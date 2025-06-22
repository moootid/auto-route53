package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// RecordConfig defines the structure for each DNS record to be updated.
type RecordConfig struct {
	ZoneID     string `json:"zone_id"`
	RecordName string `json:"record_name"`
}

// AppConfig holds the overall application configuration.
type AppConfig struct {
	SleepTime       time.Duration
	RecordsToUpdate []RecordConfig
}

// FIX: The path to the IP state file is now inside the 'data' directory.
const ipFileName = "data/last_ip.txt"

// getPublicIP fetches the current public IP address.
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
	return string(ipBytes[:len(ipBytes)-1]), nil
}

// getStoredIP reads the last known IP address from the state file.
func getStoredIP() (string, error) {
	data, err := os.ReadFile(ipFileName)
	if os.IsNotExist(err) {
		log.Println("IP file not found. A new one will be created.")
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read IP file: %w", err)
	}
	return string(data), nil
}

// storeIP writes the given IP address to the state file.
func storeIP(ip string) error {
	err := os.WriteFile(ipFileName, []byte(ip), 0644)
	if err != nil {
		return fmt.Errorf("failed to write to IP file: %w", err)
	}
	return nil
}

// updateRoute53Record updates a specific Route 53 'A' record.
func updateRoute53Record(ctx context.Context, client *route53.Client, zoneID, recordName, newIP string) error {
	log.Printf("Attempting to update %s in Zone ID %s to %s...", recordName, zoneID, newIP)

	input := &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: aws.String(fmt.Sprintf("Automatic DDNS update for %s", recordName)),
			Changes: []types.Change{
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: aws.String(recordName),
						Type: types.RRTypeA,
						TTL:  aws.Int64(300),
						ResourceRecords: []types.ResourceRecord{
							{Value: aws.String(newIP)},
						},
					},
				},
			},
		},
	}

	_, err := client.ChangeResourceRecordSets(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to update Route 53 record %s: %w", recordName, err)
	}

	log.Printf("Successfully sent update request for %s.", recordName)
	return nil
}

// loadConfig loads configuration from environment variables.
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

func main() {
	log.Println("Starting Go Dynamic DNS updater script...")

	appConfig, err := loadConfig()
	if err != nil {
		log.Fatalf("FATAL: Configuration error: %v", err)
	}

	awsCfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("FATAL: Failed to load AWS config: %v", err)
	}

	r53Client := route53.NewFromConfig(awsCfg)

	for {
		publicIP, err := getPublicIP()
		if err != nil {
			log.Printf("ERROR: %v", err)
			log.Printf("Retrying after %s...", appConfig.SleepTime)
			time.Sleep(appConfig.SleepTime)
			continue
		}

		storedIP, err := getStoredIP()
		if err != nil {
			log.Printf("ERROR: %v", err)
			log.Printf("Retrying after %s...", appConfig.SleepTime)
			time.Sleep(appConfig.SleepTime)
			continue
		}

		log.Printf("Current Public IP: %s, Last Known IP: %s", publicIP, storedIP)

		if publicIP == storedIP {
			log.Println("IP address has not changed. No update needed.")
		} else {
			log.Printf("IP address has changed from %s to %s. Updating records...", storedIP, publicIP)
			allUpdatesSuccessful := true
			for _, record := range appConfig.RecordsToUpdate {
				err := updateRoute53Record(context.TODO(), r53Client, record.ZoneID, record.RecordName, publicIP)
				if err != nil {
					log.Printf("ERROR: %v", err)
					allUpdatesSuccessful = false
				}
			}

			if allUpdatesSuccessful {
				log.Println("All records updated successfully. Storing new IP.")
				if err := storeIP(publicIP); err != nil {
					log.Printf("ERROR: Failed to store new IP: %v", err)
				}
			} else {
				log.Println("ERROR: One or more records failed to update. The IP will not be stored, and the script will retry on the next cycle.")
			}
		}

		log.Printf("Sleeping for %s...", appConfig.SleepTime)
		time.Sleep(appConfig.SleepTime)
	}
}

