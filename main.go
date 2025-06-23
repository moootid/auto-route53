package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/go-resty/resty/v2"
)

// --- Struct Definitions ---

type RecordConfig struct {
	ZoneID          string `json:"zone_id"`
	RecordName      string `json:"record_name"`
	TLS             bool   `json:"tls,omitempty"`
	Port            int    `json:"port,omitempty"`
	RedirectToHttps bool   `json:"redirect_to_https,omitempty"`
}

type AppConfig struct {
	SleepTime       time.Duration
	RecordsToUpdate []RecordConfig
	NPMBaseURL      string
	NPMIdentity     string
	NPMSecret       string
	ForwardHost     string
}

// Structs for NPM API
type NpmAuthResponse struct {
	Token string `json:"token"`
}
type NpmProxyHost struct {
	ID          int      `json:"id"`
	DomainNames []string `json:"domain_names"`
	ForwardHost string   `json:"forward_host"`
	ForwardPort int      `json:"forward_port"`
}

const (
	ipStateFile = "data/last_ip.txt"
)

// --- Shared Helper Functions ---

func getStoredString(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func storeString(filename, value string) error {
	return os.WriteFile(filename, []byte(value), 0644)
}

// --- DDNS Functions ---

func getPublicIP() (string, error) {
	resp, err := http.Get("https://checkip.amazonaws.com/")
	if err != nil {
		return "", fmt.Errorf("failed to get public IP: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status from IP service: %s", resp.Status)
	}
	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}
	return strings.TrimSpace(string(ipBytes)), nil
}

func updateRoute53Record(ctx context.Context, client *route53.Client, zoneID, recordName, value string) error {
	log.Printf("Attempting to UPSERT A record for %s in Zone ID %s...", recordName, zoneID)
	input := &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Comment: aws.String(fmt.Sprintf("Automatic DNS update for %s", recordName)),
			Changes: []r53types.Change{
				{
					Action: r53types.ChangeActionUpsert,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name: aws.String(recordName),
						Type: r53types.RRTypeA,
						TTL:  aws.Int64(300),
						ResourceRecords: []r53types.ResourceRecord{
							{Value: aws.String(value)},
						},
					},
				},
			},
		},
	}
	_, err := client.ChangeResourceRecordSets(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to update Route53 record %s: %w", recordName, err)
	}
	log.Printf("Successfully sent update request for %s.", recordName)
	return nil
}

// --- Nginx Proxy Manager Functions ---

type NpmClient struct {
	client    *resty.Client
	authToken string
}

func NewNpmClient(baseURL, identity, secret string) (*NpmClient, error) {
	npm := &NpmClient{
		client: resty.New().SetBaseURL(baseURL).SetDisableWarn(true),
	}
	authPayload := map[string]string{"identity": identity, "secret": secret}
	var authResponse NpmAuthResponse

	for i := 0; i < 5; i++ {
		resp, err := npm.client.R().
			SetHeader("Content-Type", "application/json").
			SetBody(authPayload).
			SetResult(&authResponse).
			Post("/api/tokens")
		if err == nil && resp.IsSuccess() {
			npm.authToken = authResponse.Token
			log.Println("NPM: Successfully authenticated with Nginx Proxy Manager.")
			return npm, nil
		}
		log.Printf("NPM: Authentication failed (attempt %d/5), retrying in 15 seconds... Status: %s", i+1, resp.Status())
		// Log URL and body for debugging
		log.Printf("NPM: Request URL: %s, Body: %s", baseURL, authPayload)
		time.Sleep(15 * time.Second)
	}
	return nil, fmt.Errorf("could not authenticate with Nginx Proxy Manager after several retries")
}

func (npm *NpmClient) findExistingProxyHost(domainName string) (*NpmProxyHost, error) {
	var hosts []NpmProxyHost
	resp, err := npm.client.R().SetAuthToken(npm.authToken).SetResult(&hosts).Get("/api/nginx/proxy-hosts")
	if err != nil {
		return nil, fmt.Errorf("failed to list proxy hosts: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("failed to list proxy hosts, status: %s", resp.Status())
	}
	for i := range hosts {
		for _, dn := range hosts[i].DomainNames {
			if dn == domainName {
				log.Printf("NPM [%s]: Found existing proxy host with ID %d.", domainName, hosts[i].ID)
				return &hosts[i], nil
			}
		}
	}
	return nil, nil // Not found
}

func (npm *NpmClient) createProxyHost(record RecordConfig, forwardHost string) error {
	log.Printf("NPM [%s]: Creating new proxy host pointing to %s:%d.", record.RecordName, forwardHost, record.Port)

	payload := map[string]interface{}{
		"domain_names":            []string{record.RecordName},
		"forward_scheme":          "http",
		"forward_host":            forwardHost,
		"forward_port":            record.Port,
		"allow_websocket_upgrade": true,
		"block_exploits":          true,
		"hsts_enabled":            record.RedirectToHttps,
		"hsts_subdomains":         record.RedirectToHttps,
		"ssl_forced":              record.RedirectToHttps,
	}

	// If TLS is requested, tell NPM to fetch a new Let's Encrypt certificate.
	if record.TLS {
		log.Printf("NPM [%s]: Requesting a new Let's Encrypt certificate.", record.RecordName)
		payload["certificate_id"] = "new"
		payload["hsts_enabled"] = true
		payload["hsts_subdomains"] = true
		payload["ssl_forced"] = true
	}

	resp, err := npm.client.R().
		SetAuthToken(npm.authToken).
		SetBody(payload).
		Post("/api/nginx/proxy-hosts")

	if err != nil {
		return fmt.Errorf("failed to create proxy host: %w", err)
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("failed to create proxy host, status: %s, body: %s", resp.Status(), resp.String())
	}

	log.Printf("NPM [%s]: Successfully created proxy host.", record.RecordName)
	return nil
}

func manageNginxProxy(record RecordConfig, npmClient *NpmClient, forwardHost string) {
	log.Printf("NPM [%s]: Starting proxy management.", record.RecordName)
	existingHost, err := npmClient.findExistingProxyHost(record.RecordName)
	if err != nil {
		log.Printf("NPM [%s] ERROR: %v", record.RecordName, err)
		return
	}
	if existingHost == nil {
		err := npmClient.createProxyHost(record, forwardHost)
		if err != nil {
			log.Printf("NPM [%s] ERROR: %v", record.RecordName, err)
		}
	} else {
		log.Printf("NPM [%s]: Proxy host already exists. Skipping creation.", record.RecordName)
	}
}

// --- Main Application Logic ---

func loadConfig() (*AppConfig, error) {
	sleepTimeStr := os.Getenv("SLEEP_TIME")
	if sleepTimeStr == "" {
		sleepTimeStr = "300"
	}
	sleepTime, err := time.ParseDuration(sleepTimeStr + "s")
	if err != nil {
		return nil, fmt.Errorf("invalid SLEEP_TIME format: %w", err)
	}

	recordsJSON := os.Getenv("RECORDS_TO_UPDATE")
	if recordsJSON == "" {
		return nil, fmt.Errorf("RECORDS_TO_UPDATE environment variable not set or empty")
	}
	var records []RecordConfig
	if err := json.Unmarshal([]byte(recordsJSON), &records); err != nil {
		return nil, fmt.Errorf("failed to parse RECORDS_TO_UPDATE JSON: %w", err)
	}

	return &AppConfig{
		SleepTime:       sleepTime,
		RecordsToUpdate: records,
		NPMBaseURL:      os.Getenv("NPM_URL"),
		NPMIdentity:     os.Getenv("NPM_IDENTITY"),
		NPMSecret:       os.Getenv("NPM_SECRET"),
		ForwardHost:     os.Getenv("FORWARD_HOST_IP"),
	}, nil
}

func runDDNSLoop(ctx context.Context, appConfig *AppConfig, r53Client *route53.Client) {
	for {
		publicIP, err := getPublicIP()
		if err != nil {
			log.Printf("DDNS ERROR: %v", err)
		} else {
			storedIP, _ := getStoredString(ipStateFile)
			log.Printf("DDNS Check - Public IP: %s, Stored IP: %s", publicIP, storedIP)
			if publicIP != storedIP {
				log.Printf("DDNS: IP address has changed to %s. Updating all 'A' records...", publicIP)
				allUpdated := true
				for _, record := range appConfig.RecordsToUpdate {
					if err := updateRoute53Record(ctx, r53Client, record.ZoneID, record.RecordName, publicIP); err != nil {
						log.Printf("DDNS ERROR for %s: %v", record.RecordName, err)
						allUpdated = false
					}
				}
				if allUpdated {
					log.Println("DDNS: All records updated successfully. Storing new IP.")
					if err := storeString(ipStateFile, publicIP); err != nil {
						log.Printf("DDNS ERROR: Failed to store new IP: %v", err)
					}
				}
			} else {
				log.Println("DDNS: IP has not changed.")
			}
		}
		log.Printf("DDNS: Sleeping for %s...", appConfig.SleepTime)
		time.Sleep(appConfig.SleepTime)
	}
}

func main() {
	log.Println("Starting Go Dynamic DNS, TLS, and Proxy automation script...")
	var wg sync.WaitGroup

	appConfig, err := loadConfig()
	if err != nil {
		log.Fatalf("FATAL: Configuration error: %v", err)
	}

	awsCfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("FATAL: Failed to load AWS config: %v", err)
	}
	r53Client := route53.NewFromConfig(awsCfg)

	var npmClient *NpmClient
	if appConfig.NPMBaseURL != "" && appConfig.NPMIdentity != "" {
		npmClient, err = NewNpmClient(appConfig.NPMBaseURL, appConfig.NPMIdentity, appConfig.NPMSecret)
		if err != nil {
			log.Printf("FATAL: Could not connect to Nginx Proxy Manager: %v. Proxy features will be disabled.", err)
		}
	}

	// Goroutine for the continuous DDNS loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDDNSLoop(context.Background(), appConfig, r53Client)
	}()

	// Launch one-time proxy setup tasks for each record
	for _, record := range appConfig.RecordsToUpdate {
		// Manage Nginx Proxy if port is specified and NPM is configured
		if record.Port > 0 && npmClient != nil {
			rec := record // Create a new variable for the goroutine to avoid closure issues
			if appConfig.ForwardHost == "" {
				log.Printf("NPM [%s]: Skipping proxy setup because FORWARD_HOST_IP is not set.", rec.RecordName)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				manageNginxProxy(rec, npmClient, appConfig.ForwardHost)
			}()
		}
	}

	log.Println("Application running. All startup tasks launched.")
	wg.Wait()
}
