# AWS Route 53 DDNS Updater (Go)

A simple, lightweight, and efficient containerized Go application that automatically updates AWS Route 53 'A' records with the host's current public IP address. It acts as a Dynamic DNS (DDNS) client, perfect for home servers, IoT devices, or any machine with a dynamic public IP.

This project is designed to be "set-it-and-forget-it." Configure it once with environment variables, run it, and it will keep your DNS records in sync.

**Project URL:** <https://github.com/moootid/awsroute53updater-go>

**Docker Hub Image:** `moootid/awsroute53updater-go:latest`

## Features

* **Automatic IP Detection:** Fetches the current public IP from `https://checkip.amazonaws.com/`.

* **Multi-Record Support:** Updates multiple DNS records across different hosted zones in a single run.

* **Efficient:** Uses a local state file (`last_ip.txt`) to store the last known IP, ensuring it only makes AWS API calls when the IP address has actually changed.

* **Container-First Design:** Optimized for container deployment with a minimal, multi-stage Docker build resulting in a tiny, secure image.

* **Secure:** Runs as a non-root user inside the container for enhanced security.

* **Easy Configuration:** All configuration is managed via environment variables.

## Deployment

You can deploy this application using Docker Compose (recommended) or a standard `docker run` command.

### Environment Variables

Before you deploy, you need to configure the following environment variables.

| **Variable** | **Description** | **Example** |
| --- | --- | --- |
| `AWS_ACCESS_KEY_ID` | Your AWS access key. **Best Practice:** If running on EC2/ECS, use an IAM Role instead and leave this blank. | `AKIAIOSFODNN7EXAMPLE` |
| `AWS_SECRET_ACCESS_KEY` | Your AWS secret key. | `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` |
| `AWS_REGION` | The AWS region where your Route 53 zones are managed. | `us-east-1` |
| `SLEEP_TIME` | The interval in seconds between checking for an IP address change. | `300` (for 5 minutes) |
| `RECORDS_TO_UPDATE` | A **single-line JSON array** of objects, where each object defines a `zone_id` and the `record_name` to update. | `[{"zone_id":"YOURZONEID1","record_name":"home.domain.com"},{"zone_id":"YOURZONEID2","record_name":"dev.domain.com"}]` |

### Method 1: Deploy with Docker Compose (Recommended)

This is the simplest way to get started.

**1. Create Project Files:**

Create a directory for your project and add the following two files:

`docker-compose.yml`:
```ini
services:
  aws-dns-updater:
    # Use the pre-built image from Docker Hub
    image: moootid/awsroute53updater-go:latest
    container_name: aws-dns-updater
    restart: unless-stopped
    # Load environment variables from the .env file
    env_file:
      - .env
    # Mount a local directory to persist the IP state file
    volumes:
      - ./app_data:/app/data

```
`.env`:
```ini
# --- AWS Credentials ---
AWS_ACCESS_KEY_ID=YOUR_AWS_ACCESS_KEY
AWS_SECRET_ACCESS_KEY=YOUR_AWS_SECRET_KEY
AWS_REGION=us-east-1

# --- Script Configuration ---
SLEEP_TIME=300

# --- DNS Records ---
# IMPORTANT: This must be a single line with no extra spaces.
RECORDS_TO_UPDATE=[{"zone_id":"YOUR_ZONE_ID","record_name":"your.domain.com"}]
```

**2. Create Data Directory:**

The container needs a place to store its state file. Create a local directory for it:

```
mkdir app_data
```

**3. Launch:**

Run the following command from your project directory:

```
docker-compose up -d
```

The container will start in the background and begin monitoring your IP address.

### Method 2: Deploy with `docker run`

If you prefer not to use Docker Compose, you can run the container directly.

**1. Create `.env` file and `app_data` directory:**

Follow steps 1 and 2 from the Docker Compose method to create your `.env` configuration file and `app_data` directory.

**2. Run the Container:**

Execute the following `docker run` command from your project directory. It mounts the `app_data` directory and passes the `.env` file for configuration.

```
docker run -d \
  --name aws-dns-updater \
  --restart unless-stopped \
  -e AWS_ACCESS_KEY_ID=YOUR_AWS_ACCESS_KEY \
  -e AWS_SECRET_ACCESS_KEY=YOUR_AWS_SECRET_KEY \
  -e AWS_REGION=us-east-1 \
  -e SLEEP_TIME=300 \
  -e RECORDS_TO_UPDATE=[{"zone_id":"Z0123456789ABCDEFGHIJ","record_name":"home.yourdomain.com"},{"zone_id":"Z9876543210ZYXWVUTSRQ","record_name":"another.subdomain.com"}] \
  -v "$(pwd)/app_data:/app/data" \
  moootid/awsroute53updater-go:latest
```

## Required IAM Permissions

For security, create a dedicated IAM user with the minimum required permissions. The application only needs to be able to create and update DNS records.

Attach the following policy to your IAM user or role:

```
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": "route53:ChangeResourceRecordSets",
            "Resource": "arn:aws:iam::*:hostedzone/*"
        }
    ]
}
```

**Note:** For enhanced security, you can replace `*` in the `Resource` ARN with the specific Hosted Zone IDs you are updating.

## Author

This project was created by **Mohammed Almalki**.