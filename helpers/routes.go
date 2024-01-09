package helpers

import (
	"fmt"

	"github.com/IBM/ibm-cos-sdk-go/aws"
	"github.com/IBM/ibm-cos-sdk-go/service/s3"
	"github.com/gofiber/fiber/v2"
)

func SetupRoutes(app *fiber.App) {
	app.Get("/", func(c *fiber.Ctx) error {
		return c.Render("index", fiber.Map{})
	})

	app.Post("/upload", func(c *fiber.Ctx) error {
		fmt.Println("Upload endpoint hit")
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}

		// Get all files from "documents" key:
		files := form.File["documents"]

		// Initialize IBM S3 client
		client, err := InitIBMs3()
		if err != nil {
			return err
		}

		// Retrieve the list of available buckets
		bklist, err := client.ListBuckets(nil)
		if err != nil {
			fmt.Printf("Unable to list buckets, %v", err)
		}

		// Check if there are any buckets
		if len(bklist.Buckets) == 0 {
			return fmt.Errorf("no buckets available")
		}

		// Use the first bucket
		bucket := bklist.Buckets[0].Name

		for _, fileHeader := range files {
			// Open the file
			file, err := fileHeader.Open()
			if err != nil {
				return err
			}
			defer file.Close()

			// Save the file to IBM Cloud Object Storage
			_, err = client.PutObject(&s3.PutObjectInput{
				Bucket: bucket,
				Key:    aws.String(fileHeader.Filename),
				Body:   file,
			})
			if err != nil {
				return err
			}
		}

		return c.SendString("Upload success")
	})
}
