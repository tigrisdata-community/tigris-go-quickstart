package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/joho/godotenv"
)

var svc *s3.Client

// Embed the public directory
//
//go:embed public
var publicFiles embed.FS

func main() {
	// Load environment variables
	godotenv.Load()

	// Load AWS SDK configuration
	sdkConfig, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Printf("Couldn't load default configuration. Here's why: %v\n", err)
		return
	}

	// Create S3 service client
	svc = s3.NewFromConfig(sdkConfig, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://fly.storage.tigris.dev")
		o.Region = "auto"
	})

	// Configure routes and handlers
	http.HandleFunc("/api/files", GetFilesHandler)
	http.HandleFunc("/api/upload_files", UploadFilesHandler)
	http.HandleFunc("/api/delete_file", DeleteFileHandler)

	// Serve static files
	sub, err := fs.Sub(publicFiles, "public")
	if err != nil {
		log.Fatal(err)
	}
	fs := http.FileServer(http.FS(sub))
	http.Handle("/", fs)

	// Start the server
	log.Print("Listening on :8080...")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal(err)
	}
}

type UploadFileRequest struct {
	Data string `json:"data"`
	Name string `json:"name"`
}

type UploadFileResponse struct {
	ImageUrl string `json:"imageUrl"`
}

// UploadFilesHandler handles the upload of files to Tigris
func UploadFilesHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the request body into the UploadFileRequest struct
	var req UploadFileRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Decode the base64 encoded file data
	fileData := strings.Split(req.Data, ",")[1]
	decode, err := base64.StdEncoding.DecodeString(fileData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Upload the file to Tigris
	_, err = svc.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(os.Getenv("BUCKET_NAME")),
		Key:    aws.String(req.Name),
		Body:   bytes.NewReader(decode),
	})
	if err != nil {
		log.Printf("Failed to upload data to S3: %v\n", err)
	}

	// Generate a presigned URL for the uploaded file
	presignClient := s3.NewPresignClient(svc)
	presignedUrl, err := presignClient.PresignGetObject(context.Background(),
		&s3.GetObjectInput{
			Bucket: aws.String(os.Getenv("BUCKET_NAME")),
			Key:    aws.String(req.Name),
		},
		s3.WithPresignExpires(time.Hour*1))
	if err != nil {
		log.Fatal(err)
	}

	// Return the presigned URL in the response
	res := UploadFileResponse{
		ImageUrl: presignedUrl.URL,
	}
	jbytes, err := json.Marshal(res)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(jbytes)
}

type GetFilesResponseItem struct {
	Key          string `json:"Key"`
	Url          string `json:"Url"`
	LastModified string `json:"LastModified"`
}

// GetFilesHandler handles the retrieval of files from Tigris
func GetFilesHandler(w http.ResponseWriter, r *http.Request) {
	// Create a request to list objects in the bucket
	req := &s3.ListObjectsV2Input{
		Bucket: aws.String(os.Getenv("BUCKET_NAME")),
	}

	// Loop through the objects in the bucket
	isTruncated := true
	items := []GetFilesResponseItem{}
	for isTruncated {
		// List objects in the bucket
		resp, err := svc.ListObjectsV2(context.TODO(), req)
		if err != nil {
			log.Printf("Failed to list objects: %v\n", err)
			return
		}

		// Generate presigned URLs for each object
		for _, item := range resp.Contents {
			presignClient := s3.NewPresignClient(svc)
			presignedUrl, err := presignClient.PresignGetObject(context.Background(),
				&s3.GetObjectInput{
					Bucket: aws.String(os.Getenv("BUCKET_NAME")),
					Key:    item.Key,
				},
				s3.WithPresignExpires(time.Hour*1))
			if err != nil {
				log.Fatal(err)
			}

			// Append the object to the response
			items = append(items, GetFilesResponseItem{
				Key:          *item.Key,
				Url:          presignedUrl.URL,
				LastModified: item.LastModified.String(),
			})

			// Update the request to get the next page of objects
			isTruncated = *resp.IsTruncated
			req.ContinuationToken = resp.NextContinuationToken
		}
	}

	// Return the response
	jbytes, err := json.Marshal(items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(jbytes)
}

type DeleteFileRequest struct {
	Name string `json:"name"`
}

func DeleteFileHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the request body into the DeleteFileRequest struct
	var req DeleteFileRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Delete the file from Tigris
	_, err = svc.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(os.Getenv("BUCKET_NAME")),
		Key:    aws.String(req.Name),
	})
	if err != nil {
		log.Printf("Failed to delete file from S3: %v\n", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"message": "ok"}`))
}
