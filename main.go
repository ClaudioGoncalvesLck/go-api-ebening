package main

import (
	"log"

	"github.com/cgoncalveslck/go-api-ebening/helpers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/pug/v2"
	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Print("No .env file found")
	}
}

func main() {
	app := fiber.New(
		fiber.Config{
			Views: pug.New("./templates", ".html"),
		})

	helpers.SetupRoutes(app)
	log.Fatal(app.Listen(":3000"))
}
