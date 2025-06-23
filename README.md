# auto-route53

[](https://www.google.com/search?q=https://hub.docker.com/r/moootid/auto-route53)
[](https://www.google.com/search?q=https://github.com/moootid/auto-route53/stargazers)
[](https://www.google.com/search?q=https://github.com/moootid/auto-route53/blob/main/LICENSE)

An all-in-one automation stack that bridges your local network with the public internet. It provides a complete, containerized solution that automates dynamic DNS via AWS Route 53, configures Nginx Proxy Manager as a reverse proxy, and handles Let's Encrypt SSL certificate management.

It's the ideal "set-it-and-forget-it" solution for homelabs and self-hosted projects.

**Project URL:** [https://github.com/moootid/auto-route53](https://www.google.com/search?q=httpss://github.com/moootid/auto-route53)
**Docker Hub Image:** `moootid/auto-route53:latest`

-----

## Features

  - **Dynamic DNS:** A Go application keeps your AWS Route 53 'A' records pointing to your machine's dynamic public IP.
  - **Automated TLS:** The app automatically instructs Nginx Proxy Manager to request and manage **Let's Encrypt SSL certificates** on a per-domain basis.
  - **Automated Reverse Proxy:** The app automatically configures Nginx Proxy Manager via its API, creating proxy hosts to route incoming traffic to your local services (e.g., other Docker containers) based on domain name.
  - **Granular Control:** Configure DNS, TLS, and proxy settings for each domain individually in a single configuration file.
  - **State-Aware:** Uses a local state file to prevent unnecessary API calls to AWS.
  - **Container-First Design:** Optimized with a minimal, multi-stage Docker build.
  - **Secure:** Runs as a non-root user inside the container for enhanced security.

-----

## How It Works

The entire stack is defined in a `docker-compose.yml` file. When you launch it:

1.  **Nginx Proxy Manager (NPM)** and its database start up.
2.  The **auto-route53** Go application starts.
3.  The Go app reads your `.env` configuration.
4.  It authenticates with the NPM API.
5.  It runs a one-time setup task: for each domain with a `"port"` defined, it ensures a Proxy Host is configured in NPM to forward traffic. If `"tls": true` is also set, it tells NPM to handle the entire Let's Encrypt certificate acquisition process.
6.  Finally, it enters a continuous loop to monitor your public IP and update all configured Route 53 records if it changes.


## Deployment

You can deploy this application using Docker Compose (recommended) or with individual `docker run` commands. Both methods require the same initial setup.

### Prerequisites: Configuration & Directories

Before deploying, you must create the necessary configuration file and data directories on your host machine.

**1. Create the Environment File:**

The project uses an `example.env` file as a template. Copy it to create your own configuration file:

```bash
cp example.env .env
```

Now, edit the `.env` file with your specific details. You **must** set:

  - Your `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.
  - Your `AWS_REGION`.
  - Your desired `RECORDS_TO_UPDATE` configuration.
  - The `FORWARD_HOST_IP` (the private IP of the machine Docker is running on, e.g., `192.168.1.100`).
  - Your desired credentials for `NPM_IDENTITY` and `NPM_SECRET`.

**2. Create Data Directories:**

The stack uses persistent volumes to store all state data. Create the necessary local folders:

```bash
mkdir updater_data npm_data npm_letsencrypt
```

-----

### Method 1: Deploy with Docker Compose (Recommended)

This is the simplest and recommended way to run the entire stack.

**1. Check your `docker-compose.yml`:**

Ensure your `docker-compose.yml` file is present in your project directory.

**2. Launch the Stack:**

Run a single command from your project directory to build the image and run all services in the background:

```bash
docker-compose up --build -d
```

The stack is now running. You can proceed to the "First-Time Nginx Proxy Manager Setup" section below.

-----

### Method 2: Deploy with Docker Run Commands

This method is more verbose and is for users who prefer not to use Docker Compose. It requires running several commands in the correct order.

**1. Create a Docker Network:**

The containers need to communicate with each other. Create a dedicated network for them:

```bash
docker network create npm_network
```

**2. Run the Nginx Proxy Manager Container:**

This command starts the Nginx Proxy Manager container, connects it to the network, and maps its data volumes and ports.

```bash
docker run -d \
  --name nginx-proxy-manager-app \
  --network npm_network \
  --restart unless-stopped \
  -p 80:80 \
  -p 443:443 \
  -p 81:81 \
  -e NPM_IDENTITY="${NPM_IDENTITY}" \
  -e NPM_SECRET="${NPM_SECRET}" \
  -e DISABLE_IPV6=true \
  -v "$(pwd)/npm_data:/data" \
  -v "$(pwd)/npm_letsencrypt:/etc/letsencrypt" \
  jc21/nginx-proxy-manager:latest
```

**3. Run the auto-route53 Container:**

This command starts the main application, connects it to the same network, and passes all environment variables from your `.env` file.

```bash
docker run -d \
  --name auto-route53 \
  --network npm_network \
  --restart unless-stopped \
  --env-file .env \
  -v "$(pwd)/updater_data:/app/data" \
  moootid/auto-route53:latest
```

The stack is now running.

-----

### First-Time Nginx Proxy Manager Setup

This step is required regardless of which deployment method you chose.

1.  After a minute, navigate to your server's IP on port 81 (e.g., `http://192.168.1.100:81`).
2.  Log in with the credentials you set for `NPM_IDENTITY` and `NPM_SECRET` in your `.env` file.
3.  Nginx Proxy Manager will immediately prompt you to change your password. **Do this now.**
4.  Update the `NPM_SECRET` variable in your `.env` file with your new password.
5.  Restart the `auto-route53` container to apply the new credentials:
    ```bash
    # If you used Docker Compose
    docker-compose restart auto-route53

    # If you used Docker Run
    docker restart auto-route53
    ```

Your setup is now complete and fully automated\!
## Environment Variable Reference

| Variable | Description |
| --- | --- |
| `AWS_ACCESS_KEY_ID` | Your AWS access key for Route 53. |
| `AWS_SECRET_ACCESS_KEY`| Your AWS secret key for Route 53. |
| `AWS_REGION` | The AWS region where your Route 53 zones are managed. |
| `SLEEP_TIME` | The interval in seconds between checking for an IP address change. Defaults to 300. |
| `RECORDS_TO_UPDATE` | A **single-line JSON array** of objects defining the domains to manage. |
| `NPM_URL` | The internal Docker network URL for the Nginx Proxy Manager API. **Should be `http://npm-app:81`**. |
| `NPM_IDENTITY` | The email address used to log in to Nginx Proxy Manager. |
| `NPM_SECRET` | The password for your Nginx Proxy Manager user. |
| `FORWARD_HOST_IP` | The private IP address of the host machine where your target applications/ports are running. |

### `RECORDS_TO_UPDATE` Structure

Each object in the JSON array can have the following keys:

  - `zone_id` (required): The AWS Route 53 Hosted Zone ID.
  - `record_name` (required): The domain or subdomain name.
  - `port` (optional): If present, a reverse proxy host will be created in NPM for this port.
  - `tls` (optional): If `true`, NPM will be instructed to request a Let's Encrypt certificate for the domain.
  - `redirect_to_https` (optional): If `true`, forces an HTTPS redirect in NPM.

-----

## Required IAM Permissions

For security, create a dedicated IAM user with the minimum required permissions. The application only needs to be able to create and update DNS records in Route 53.

Attach the following policy to your IAM user or role:

```json
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

**Note:** For enhanced security, you can replace `*` in the `Resource` ARN with your specific Hosted Zone IDs.

-----

## Author

This project was created by **Mohammed Almalki** @moootid.