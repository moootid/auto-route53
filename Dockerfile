# --- Stage 1: Builder ---
# Use the official Go image to build the application.
# Using a specific version is a good practice.
FROM golang:1.24-alpine as builder

# Set the working directory inside the container
WORKDIR /app

# Copy go module and sum files
COPY go.mod go.sum ./

# Download all dependencies.
RUN go mod download

# Copy the source code into the container
COPY main.go .

# Build the Go app.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /go-ddns-updater main.go

# --- Stage 2: Final ---
# Use a minimal, non-root base image for the final container.
FROM alpine:latest

# Alpine needs ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /app

# Copy only the compiled binary from the builder stage into the current directory
COPY --from=builder /go-ddns-updater .

# Create and switch to a non-root user
# This is a security best practice.
RUN adduser -D appuser

# Create a directory for persistent data.
# This directory will be the target for our volume mount.
RUN mkdir /app/data && chown appuser:appuser /app/data

# Switch to the non-root user
USER appuser

# Set the entrypoint for the container
ENTRYPOINT ["./go-ddns-updater"]
