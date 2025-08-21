package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rs/zerolog/log"
)

// S3Config holds S3 configuration for a user
type S3Config struct {
	Enabled       bool
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	PathStyle     bool
	PublicURL     string
	MediaDelivery string
	RetentionDays int
	EnableACL     bool // Enable setting ACL on uploaded objects (for legacy buckets)
}

// S3Manager manages S3 operations
type S3Manager struct {
	mu      sync.RWMutex
	clients map[string]*s3.Client
	configs map[string]*S3Config
}

// Global S3 manager instance
var s3Manager = &S3Manager{
	clients: make(map[string]*s3.Client),
	configs: make(map[string]*S3Config),
}

// GetS3Manager returns the global S3 manager instance
func GetS3Manager() *S3Manager {
	return s3Manager
}

// InitializeS3Client creates or updates S3 client for a user
func (m *S3Manager) InitializeS3Client(userID string, config *S3Config) error {
	if !config.Enabled {
		m.RemoveClient(userID)
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Use global environment variables for credentials
	globalAccessKey := os.Getenv(S3_GLOBAL_ACCESS_KEY)
	globalSecretKey := os.Getenv(S3_GLOBAL_SECRET_KEY)
	
	// Fallback to user-specific credentials if global ones are not set
	accessKey := globalAccessKey
	secretKey := globalSecretKey
	if accessKey == "" {
		accessKey = config.AccessKey
	}
	if secretKey == "" {
		secretKey = config.SecretKey
	}

	// Validate that we have credentials
	if accessKey == "" || secretKey == "" {
		return fmt.Errorf("S3 credentials not available - set %s and %s environment variables or configure user-specific credentials", S3_GLOBAL_ACCESS_KEY, S3_GLOBAL_SECRET_KEY)
	}

	// Create custom credentials provider
	credProvider := credentials.NewStaticCredentialsProvider(
		accessKey,
		secretKey,
		"",
	)

	// Use global environment variables for region/endpoint/bucket if available
	region := config.Region
	if globalRegion := os.Getenv(S3_GLOBAL_REGION); globalRegion != "" {
		region = globalRegion
	}

	endpoint := config.Endpoint
	if globalEndpoint := os.Getenv(S3_GLOBAL_ENDPOINT); globalEndpoint != "" {
		endpoint = globalEndpoint
	}

	// Clean endpoint if it contains bucket name (common misconfiguration)
	if endpoint != "" && strings.Contains(endpoint, config.Bucket+".") {
		// Remove bucket name from endpoint
		endpoint = strings.Replace(endpoint, config.Bucket+".", "", 1)
		log.Warn().
			Str("userID", userID).
			Str("originalEndpoint", os.Getenv(S3_GLOBAL_ENDPOINT)).
			Str("cleanedEndpoint", endpoint).
			Str("bucket", config.Bucket).
			Msg("Cleaned bucket name from S3 endpoint - endpoint should not contain bucket name")
	}

	// Update bucket from global environment if available
	if globalBucket := os.Getenv(S3_GLOBAL_BUCKET); globalBucket != "" && config.Bucket == "" {
		config.Bucket = globalBucket // Update the config to use global bucket
	}

	// Configure S3 client
	cfg := aws.Config{
		Region:      region,
		Credentials: credProvider,
	}

	if endpoint != "" {
		customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			if service == s3.ServiceID {
				return aws.Endpoint{
					URL:               endpoint,
					HostnameImmutable: config.PathStyle,
				}, nil
			}
			return aws.Endpoint{}, &aws.EndpointNotFoundError{}
		})
		cfg.EndpointResolverWithOptions = customResolver
	}

	// Force path-style for buckets with dots in their names to avoid SSL certificate issues
	usePathStyle := config.PathStyle
	if strings.Contains(config.Bucket, ".") {
		usePathStyle = true
		log.Info().
			Str("userID", userID).
			Str("bucket", config.Bucket).
			Msg("Bucket name contains dots, forcing path-style URLs to avoid SSL certificate issues")
	}

	// Create S3 client
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = usePathStyle
	})

	m.clients[userID] = client
	m.configs[userID] = config

	log.Info().
		Str("userID", userID).
		Str("bucket", config.Bucket).
		Str("region", region).
		Str("endpoint", endpoint).
		Bool("using_global_credentials", globalAccessKey != "").
		Bool("using_global_bucket", os.Getenv(S3_GLOBAL_BUCKET) != "").
		Msg("S3 client initialized")
	return nil
}

// RemoveClient removes S3 client for a user
func (m *S3Manager) RemoveClient(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.clients, userID)
	delete(m.configs, userID)
}

// GetClient returns S3 client for a user
func (m *S3Manager) GetClient(userID string) (*s3.Client, *S3Config, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, clientOk := m.clients[userID]
	config, configOk := m.configs[userID]

	return client, config, clientOk && configOk
}

// GenerateS3Key generates S3 object key based on message metadata
func (m *S3Manager) GenerateS3Key(userID, contactJID, messageID string, mimeType string, isIncoming bool) string {
	// Determine direction
	direction := "outbox"
	if isIncoming {
		direction = "inbox"
	}

	// Clean contact JID
	contactJID = strings.ReplaceAll(contactJID, "@", "_")
	contactJID = strings.ReplaceAll(contactJID, ":", "_")

	// Get current time
	now := time.Now()
	year := now.Format("2006")
	month := now.Format("01")
	day := now.Format("02")

	// Determine media type folder
	mediaType := "documents"
	if strings.HasPrefix(mimeType, "image/") {
		mediaType = "images"
	} else if strings.HasPrefix(mimeType, "video/") {
		mediaType = "videos"
	} else if strings.HasPrefix(mimeType, "audio/") {
		mediaType = "audio"
	}

	// Get file extension
	ext := ".bin"
	switch {
	case strings.Contains(mimeType, "jpeg"), strings.Contains(mimeType, "jpg"):
		ext = ".jpg"
	case strings.Contains(mimeType, "png"):
		ext = ".png"
	case strings.Contains(mimeType, "gif"):
		ext = ".gif"
	case strings.Contains(mimeType, "webp"):
		ext = ".webp"
	case strings.Contains(mimeType, "mp4"):
		ext = ".mp4"
	case strings.Contains(mimeType, "webm"):
		ext = ".webm"
	case strings.Contains(mimeType, "ogg"):
		ext = ".ogg"
	case strings.Contains(mimeType, "opus"):
		ext = ".opus"
	case strings.Contains(mimeType, "pdf"):
		ext = ".pdf"
	case strings.Contains(mimeType, "doc"):
		if strings.Contains(mimeType, "docx") {
			ext = ".docx"
		} else {
			ext = ".doc"
		}
	}

	// Build S3 key
	key := fmt.Sprintf("users/%s/%s/%s/%s/%s/%s/%s/%s%s",
		userID,
		direction,
		contactJID,
		year,
		month,
		day,
		mediaType,
		messageID,
		ext,
	)

	return key
}

// UploadToS3 uploads file to S3 and returns the key
func (m *S3Manager) UploadToS3(ctx context.Context, userID string, key string, data []byte, mimeType string) error {
	client, config, ok := m.GetClient(userID)
	if !ok {
		return fmt.Errorf("S3 client not initialized for user %s", userID)
	}

	// Set content type and cache headers for preview
	contentType := mimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Calculate expiration time based on retention days
	var expires *time.Time
	if config.RetentionDays > 0 {
		expirationTime := time.Now().Add(time.Duration(config.RetentionDays) * 24 * time.Hour)
		expires = &expirationTime
	}

	input := &s3.PutObjectInput{
		Bucket:       aws.String(config.Bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(data),
		ContentType:  aws.String(contentType),
		CacheControl: aws.String("public, max-age=3600"),
	}

	// Only set ACL if explicitly enabled (for legacy bucket compatibility)
	if config.EnableACL {
		input.ACL = types.ObjectCannedACLPublicRead
	}

	if expires != nil {
		input.Expires = expires
	}

	// Add content disposition for inline preview
	if strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "video/") || mimeType == "application/pdf" {
		input.ContentDisposition = aws.String("inline")
	}

	_, err := client.PutObject(ctx, input)
	if err != nil {
		log.Error().
			Str("userID", userID).
			Str("key", key).
			Str("bucket", config.Bucket).
			Str("mimeType", mimeType).
			Int("size", len(data)).
			Err(err).
			Msg("Failed to upload file to S3")
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	log.Info().
		Str("userID", userID).
		Str("key", key).
		Str("bucket", config.Bucket).
		Str("mimeType", mimeType).
		Int("size", len(data)).
		Msg("File successfully uploaded to S3")

	return nil
}

// GetPublicURL generates public URL for S3 object
func (m *S3Manager) GetPublicURL(userID, key string) string {
	_, config, ok := m.GetClient(userID)
	if !ok {
		return ""
	}

	// Use custom public URL if configured
	if config.PublicURL != "" {
		url := fmt.Sprintf("%s/%s/%s", strings.TrimRight(config.PublicURL, "/"), config.Bucket, key)
		log.Debug().
			Str("userID", userID).
			Str("bucket", config.Bucket).
			Str("key", key).
			Str("publicURL", config.PublicURL).
			Str("generatedURL", url).
			Msg("Generated URL using custom public URL")
		return url
	}

	// Get resolved endpoint and region (may come from environment variables)
	endpoint := config.Endpoint
	if globalEndpoint := os.Getenv(S3_GLOBAL_ENDPOINT); globalEndpoint != "" {
		endpoint = globalEndpoint
	}

	region := config.Region
	if globalRegion := os.Getenv(S3_GLOBAL_REGION); globalRegion != "" {
		region = globalRegion
	}

	// Force path-style for buckets with dots in their names to avoid SSL certificate issues
	usePathStyle := config.PathStyle
	if strings.Contains(config.Bucket, ".") {
		usePathStyle = true
	}

	log.Debug().
		Str("userID", userID).
		Str("bucket", config.Bucket).
		Str("endpoint", endpoint).
		Str("region", region).
		Bool("usePathStyle", usePathStyle).
		Msg("S3 URL generation parameters")

	var generatedURL string

	// Generate AWS S3 URL
	if strings.Contains(endpoint, "amazonaws.com") {
		if usePathStyle {
			// Path-style URL for AWS S3
			generatedURL = fmt.Sprintf("https://s3.%s.amazonaws.com/%s/%s",
				region,
				config.Bucket,
				key)
		} else {
			// Virtual hosted-style URL for AWS S3
			generatedURL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s",
				config.Bucket,
				region,
				key)
		}
	} else if endpoint != "" {
		// For other S3-compatible services
		if usePathStyle {
			generatedURL = fmt.Sprintf("%s/%s/%s",
				strings.TrimRight(endpoint, "/"),
				config.Bucket,
				key)
		} else {
			endpointClean := strings.TrimPrefix(endpoint, "https://")
			endpointClean = strings.TrimPrefix(endpointClean, "http://")
			generatedURL = fmt.Sprintf("https://%s.%s/%s", config.Bucket, endpointClean, key)
		}
	} else {
		// Default AWS S3 URL when no endpoint is specified
		if usePathStyle {
			// Path-style URL for AWS S3
			generatedURL = fmt.Sprintf("https://s3.%s.amazonaws.com/%s/%s",
				region,
				config.Bucket,
				key)
		} else {
			// Virtual hosted-style URL for AWS S3
			generatedURL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s",
				config.Bucket,
				region,
				key)
		}
	}

	log.Info().
		Str("userID", userID).
		Str("bucket", config.Bucket).
		Str("key", key).
		Str("endpoint", endpoint).
		Str("region", region).
		Bool("usePathStyle", usePathStyle).
		Str("generatedURL", generatedURL).
		Msg("Generated S3 public URL")

	return generatedURL
}

// TestConnection tests S3 connection
func (m *S3Manager) TestConnection(ctx context.Context, userID string) error {
	client, config, ok := m.GetClient(userID)
	if !ok {
		return fmt.Errorf("S3 client not initialized for user %s", userID)
	}

	// Try to list objects with max 1 result
	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(config.Bucket),
		MaxKeys: aws.Int32(1),
	}

	_, err := client.ListObjectsV2(ctx, input)
	return err
}

// ProcessMediaForS3 handles the complete media upload process
func (m *S3Manager) ProcessMediaForS3(ctx context.Context, userID, contactJID, messageID string,
	data []byte, mimeType string, fileName string, isIncoming bool) (map[string]interface{}, error) {

	// Generate S3 key
	key := m.GenerateS3Key(userID, contactJID, messageID, mimeType, isIncoming)

	// Upload to S3
	err := m.UploadToS3(ctx, userID, key, data, mimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to upload to S3: %w", err)
	}

	// Generate public URL
	publicURL := m.GetPublicURL(userID, key)

	// Return S3 metadata
	s3Data := map[string]interface{}{
		"url":      publicURL,
		"key":      key,
		"bucket":   m.configs[userID].Bucket,
		"size":     len(data),
		"mimeType": mimeType,
		"fileName": fileName,
	}

	return s3Data, nil
}

// DeleteAllUserObjects deletes all user files from S3
func (m *S3Manager) DeleteAllUserObjects(ctx context.Context, userID string) error {
	client, config, ok := m.GetClient(userID)
	if !ok {
		return fmt.Errorf("S3 client not initialized for user %s", userID)
	}

	prefix := fmt.Sprintf("users/%s/", userID)
	var toDelete []types.ObjectIdentifier
	var continuationToken *string

	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            aws.String(config.Bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		}
		output, err := client.ListObjectsV2(ctx, input)
		if err != nil {
			return fmt.Errorf("failed to list objects for user %s: %w", userID, err)
		}

		for _, obj := range output.Contents {
			toDelete = append(toDelete, types.ObjectIdentifier{Key: obj.Key})
			// Delete in batches of 1000 (S3 limit)
			if len(toDelete) == 1000 {
				_, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
					Bucket: aws.String(config.Bucket),
					Delete: &types.Delete{Objects: toDelete},
				})
				if err != nil {
					return fmt.Errorf("failed to delete objects for user %s: %w", userID, err)
				}
				toDelete = nil
			}
		}

		if output.IsTruncated != nil && *output.IsTruncated && output.NextContinuationToken != nil {
			continuationToken = output.NextContinuationToken
		} else {
			break
		}
	}

	// Delete any remaining objects
	if len(toDelete) > 0 {
		_, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(config.Bucket),
			Delete: &types.Delete{Objects: toDelete},
		})
		if err != nil {
			return fmt.Errorf("failed to delete objects for user %s: %w", userID, err)
		}
	}

	log.Info().Str("userID", userID).Msg("all user files removed from S3")
	return nil
}
