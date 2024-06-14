package main

import (
	"os"

	"github.com/cgoncalveslck/go-api-ebening/bot"
)

func main() {
	bot.BotToken = os.Getenv("BOT_TOKEN")
	if bot.BotToken == "" {
		panic("BOT_TOKEN environment variable is required")
	}
	bot.Run()
}
