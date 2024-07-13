package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"time"

	"github.com/cloudflare/cloudflare-go"
	"github.com/go-resty/resty/v2"
	"github.com/traefik/traefik/v3/pkg/muxer/http"

	log "github.com/sirupsen/logrus"
)

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
}

func main() {
	cf, err := cloudflare.NewWithAPIToken(os.Getenv("CLOUDFLARE_API_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	client := resty.New().
		SetBaseURL(os.Getenv("TRAEFIK_API_ENDPOINT"))

	pollCh := pollTraefikRouters(client)
	var cache []Router
	for poll := range pollCh {
		if poll.Err != nil {
			log.Fatal(poll.Err)
		}

		// skip if no changes to traefik routers
		if reflect.DeepEqual(cache, poll.Routers) {
			continue
		}

		log.Info("changes detected")

		// update the cache
		cache = poll.Routers

		ingress := []cloudflare.UnvalidatedIngressRule{}

		for _, r := range poll.Routers {
			// Only enabled routes
			if r.Status != "enabled" {
				continue
			}

			// Skip any routes with TLS configured
			if os.Getenv("TRAEFIK_PARSE_TLS") != "true" {
				if r.TLS.CertResolver != "" {
					// TODO: use better indicator for TLS
					continue
				}
			}

			// Only use routes with the tunneld entrypoint
			if !contains(r.EntryPoints, os.Getenv("TRAEFIK_ENTRYPOINT")) {
				continue
			}
			domains, err := http.ParseDomains(r.Rule)
			if err != nil {
				log.Fatal(err)
			}

			for _, domain := range domains {
				log.WithFields(log.Fields{
					"domain":  domain,
					"service": os.Getenv("TRAEFIK_SERVICE_ENDPOINT"),
				}).Info("upserting tunnel")

				no_tls := false
				http2 := false
				if os.Getenv("TRAEFIK_PARSE_TLS") == "true" {
					// Add support for HTTPS2 and do not verify TLS origin certificates
					no_tls = true
					http2 = true
				}

				// Create the ingress rule to use
				ingressRule := cloudflare.UnvalidatedIngressRule{
					Service:  os.Getenv("TRAEFIK_SERVICE_ENDPOINT"),
					Hostname: domain,
					OriginRequest: &cloudflare.OriginRequestConfig{
						HTTPHostHeader: &domain,
						NoTLSVerify:    &no_tls,
						Http2Origin:    &http2,
					},
				}
				ingress = append(ingress, ingressRule)
			}

		}

		err = updateTunnels(ctx, cf, ingress)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func pollTraefikRouters(client *resty.Client) (ch chan PollResponse) {
	ch = make(chan PollResponse)
	go func() {
		defer func() {
			close(ch)
		}()
		r := rand.New(rand.NewSource(99))
		c := time.Tick(10 * time.Second)

		for range c {
			var pollRes PollResponse

			_, pollRes.Err = client.R().
				EnableTrace().
				SetResult(&pollRes.Routers).
				Get("/api/http/routers")

			if pollRes.Err != nil {
				ch <- pollRes
				break
			}

			ch <- pollRes

			jitter := time.Duration(r.Int31n(5000)) * time.Millisecond
			time.Sleep(jitter)
		}
	}()
	return
}

func updateTunnels(ctx context.Context, cf *cloudflare.API, ingress []cloudflare.UnvalidatedIngressRule) error {
	// Create the account resource context container
	accountResource := cloudflare.AccountIdentifier(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
	zoneResource := cloudflare.ZoneIdentifier(os.Getenv("CLOUDFLARE_ZONE_ID"))

	// Get Current tunnel config
	cfg, err := cf.GetTunnelConfiguration(ctx, accountResource, os.Getenv("CLOUDFLARE_TUNNEL_ID"))
	if err != nil {
		return fmt.Errorf("unable to pull current tunnel config, %s", err.Error())
	}

	// Update config with new ingress rules
	cfg_new := cloudflare.TunnelConfiguration{
		WarpRouting:   cfg.Config.WarpRouting,
		Ingress:       ingress,
		OriginRequest: cfg.Config.OriginRequest,
	}

	cfg, err = cf.UpdateTunnelConfiguration(ctx, accountResource, cloudflare.TunnelConfigurationParams{
		TunnelID: os.Getenv("CLOUDFLARE_TUNNEL_ID"),
		Config:   cfg_new,
	})
	if err != nil {
		return fmt.Errorf("unable to update tunnel config, %s", err.Error())
	}

	log.Info("tunnel config updated")

	// Update DNS to point to new tunnel
	for _, i := range ingress {
		if i.Hostname == "" {
			continue
		}

		var proxied bool = true

		record := cloudflare.DNSRecord{
			Type:    "CNAME",
			Name:    i.Hostname,
			Content: fmt.Sprintf("%s.cfargotunnel.com", os.Getenv("CLOUDFLARE_TUNNEL_ID")),
			TTL:     1,
			Proxied: &proxied,
		}

		rec := cloudflare.CreateDNSRecordParams{
			Type:    "CNAME",
			Name:    i.Hostname,
			Content: fmt.Sprintf("%s.cfargotunnel.com", os.Getenv("CLOUDFLARE_TUNNEL_ID")),
			Proxied: &proxied,
			TTL:     1,
		}

		r, err := cf.GetDNSRecord(ctx, zoneResource, i.Hostname)
		if err != nil {
			return fmt.Errorf("err checking DNS records, %s", err.Error())
			_, err := cf.CreateDNSRecord(ctx, zoneResource, rec)
			if err != nil {
				return fmt.Errorf("unable to create DNS record, %s", err.Error())
			}
			log.WithFields(log.Fields{
				"domain": record.Name,
			}).Info("DNS created")
			continue
		}

		if r.Content != record.Content {
			update_params := cloudflare.UpdateDNSRecordParams{
				Type:    "CNAME",
				Name:    i.Hostname,
				Content: fmt.Sprintf("%s.cfargotunnel.com", os.Getenv("CLOUDFLARE_TUNNEL_ID")),
				Proxied: &proxied,
				TTL:     1,
			}
			_, err := cf.UpdateDNSRecord(ctx, zoneResource, update_params)
			if err != nil {
				return fmt.Errorf("could not update record for %s, %s", i.Hostname, err)
			}
			log.WithFields(log.Fields{
				"domain": record.Name,
			}).Info("DNS updated")
		}
	}

	// TODO: delete CNAME records with content that is _not_ in our list

	return nil
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

type PollResponse struct {
	Routers []Router
	Err     error
}

type Router struct {
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
	Rule        string   `json:"rule"`
	Status      string   `json:"status"`
	Using       []string `json:"using"`
	ServiceName string   `json:"name"`
	Provider    string   `json:"provider"`
	Middlewares []string `json:"middlewares,omitempty"`
	TLS         struct {
		CertResolver string `json:"certResolver"`
	} `json:"tls,omitempty"`
	Priority int `json:"priority,omitempty"`
}
