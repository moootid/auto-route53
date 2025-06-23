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
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// RecordConfig defines the structure for each DNS record to be updated.
// The `json:"tls,omitempty"` tag makes the field optional and default to false if not present.
type RecordConfig struct {
	ZoneID     string `json:"zone_id"`
	RecordName string `json:"record_name"`
	TLS        bool   `json:"tls,omitempty"`
}

// AppConfig holds the overall application configuration.
type AppConfig struct {
	SleepTime       time.Duration
	RecordsToUpdate []RecordConfig
}

const (
	ipStateFile        = "data/last_ip.txt"
	// Certificate state file will now be named based on the domain
	certStateFilePrefix = "data/cert_arn_"
	certValidationWait  = 15 * time.Minute
)

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

func getStoredString(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return "", nil // Not an error, just doesn't exist yet
	}
	return string(data), err
}

func storeString(filename, value string) error {
	return os.WriteFile(filename, []byte(value), 0644)
}

func updateRoute53Record(ctx context.Context, client *route53.Client, zoneID, recordName, recordType, value string) error {
	log.Printf("Attempting to UPSERT %s record for %s in Zone ID %s...", recordType, recordName, zoneID)
	input := &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Comment: aws.String(fmt.Sprintf("Automatic DNS update for %s", recordName)),
			Changes: []r53types.Change{
				{
					Action: r53types.ChangeActionUpsert,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name: aws.String(recordName),
						Type: r53types.RRType(recordType),
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


// --- Certificate Management Functions ---

func getCertStateFileName(domainName string) string {
	// Sanitize domain name for filename
	sanitized := strings.ReplaceAll(domainName, "*", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, ".", "_")
	return certStateFilePrefix + sanitized + ".txt"
}

func findExistingCertificate(ctx context.Context, client *acm.Client, domainName string) (string, error) {
	log.Printf("CERT [%s]: Checking for existing certificate", domainName)
	paginator := acm.NewListCertificatesPaginator(client, &acm.ListCertificatesInput{
		CertificateStatuses: []acmtypes.CertificateStatus{acmtypes.CertificateStatusIssued},
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to list certificates: %w", err)
		}
		for _, cert := range page.CertificateSummaryList {
			if *cert.DomainName == domainName {
				log.Printf("CERT [%s]: Found existing, issued certificate with ARN: %s", domainName, *cert.CertificateArn)
				return *cert.CertificateArn, nil
			}
		}
	}
	log.Printf("CERT [%s]: No existing issued certificate found.", domainName)
	return "", nil
}

// manageCertificateLifecycle handles the process for a single domain.
func manageCertificateLifecycle(ctx context.Context, record RecordConfig, r53Client *route53.Client, acmClient *acm.Client) {
	domainName := record.RecordName
	log.Printf("CERT [%s]: Starting certificate management process.", domainName)
	certStateFile := getCertStateFileName(domainName)

	// 1. Check local state
	arn, err := getStoredString(certStateFile)
	if err != nil {
		log.Printf("CERT [%s] ERROR: Could not read stored ARN: %v", domainName, err)
		return
	}
	if arn != "" {
		log.Printf("CERT [%s]: Found stored ARN. Process complete.", domainName)
		return
	}

	// 2. Check AWS for existing certificate
	arn, err = findExistingCertificate(ctx, acmClient, domainName)
	if err != nil {
		log.Printf("CERT [%s] ERROR: Could not check for existing certificate: %v", domainName, err)
		return
	}
	if arn != "" {
		if err := storeString(certStateFile, arn); err != nil {
			log.Printf("CERT [%s] ERROR: Found existing cert but failed to store its ARN: %v", domainName, err)
		}
		return
	}
	
	// 3. Request a new certificate
	log.Printf("CERT [%s]: Requesting new certificate...", domainName)
	reqOut, err := acmClient.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(domainName),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	if err != nil {
		log.Printf("CERT [%s] ERROR: Failed to request certificate: %v", domainName, err)
		return
	}
	certArn := *reqOut.CertificateArn
	log.Printf("CERT [%s]: Certificate requested. ARN: %s. Waiting for validation details...", domainName, certArn)

	// 4. Wait for validation details and perform DNS validation
	var validationOption *acmtypes.DomainValidation
	for start := time.Now(); time.Since(start) < certValidationWait; {
		descOut, err := acmClient.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: &certArn})
		if err != nil {
			log.Printf("CERT [%s] ERROR: Could not describe certificate: %v", domainName, err)
			return
		}
		if len(descOut.Certificate.DomainValidationOptions) > 0 {
			validationOption = &descOut.Certificate.DomainValidationOptions[0]
			if validationOption.ResourceRecord != nil {
				break
			}
		}
		log.Printf("CERT [%s]: Validation details not yet available, waiting 30 seconds...", domainName)
		time.Sleep(30 * time.Second)
	}
	if validationOption == nil || validationOption.ResourceRecord == nil {
		log.Printf("CERT [%s] ERROR: Timed out waiting for ACM validation details.", domainName)
		return
	}

	validationRecord := validationOption.ResourceRecord
	err = updateRoute53Record(ctx, r53Client, record.ZoneID, *validationRecord.Name, string(validationRecord.Type), *validationRecord.Value)
	if err != nil {
		log.Printf("CERT [%s] ERROR: Failed to create DNS validation record: %v", domainName, err)
		return
	}
	
	// 5. Wait for validation to complete
	log.Printf("CERT [%s]: DNS validation record created. Waiting for ACM to validate...", domainName)
	waiter := acm.NewCertificateValidatedWaiter(acmClient)
	err = waiter.Wait(ctx, &acm.DescribeCertificateInput{CertificateArn: &certArn}, certValidationWait)
	if err != nil {
		log.Printf("CERT [%s] ERROR: Certificate validation failed or timed out: %v", domainName, err)
		return
	}
	
	// 6. Store the final ARN
	log.Printf("CERT [%s]: Certificate successfully validated and issued!", domainName)
	if err := storeString(certStateFile, certArn); err != nil {
		log.Printf("CERT [%s] ERROR: Certificate issued but failed to store ARN: %v", domainName, err)
	}
	log.Printf("CERT [%s]: Certificate management process complete.", domainName)
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
					if err := updateRoute53Record(ctx, r53Client, record.ZoneID, record.RecordName, "A", publicIP); err != nil {
						log.Printf("DDNS ERROR: %v", err)
						allUpdated = false
					}
				}
				if allUpdated {
					if err := storeString(ipStateFile, publicIP); err != nil {
						log.Printf("DDNS ERROR: %v", err)
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
	log.Println("Starting Go Dynamic DNS updater script...")
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
	acmClient := acm.NewFromConfig(awsCfg)

	// Goroutine for the continuous DDNS loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDDNSLoop(context.Background(), appConfig, r53Client)
	}()

	// Launch a separate, one-time certificate management goroutine FOR EACH record with tls: true
	for _, record := range appConfig.RecordsToUpdate {
		if record.TLS {
			// Create a new variable for the goroutine to avoid closure issues
			rec := record
			wg.Add(1)
			go func() {
				defer wg.Done()
				manageCertificateLifecycle(context.Background(), rec, r53Client, acmClient)
			}()
		}
	}

	log.Println("Application running. DDNS loop is active.")
	wg.Wait()
}
