package main

import (
	"fmt"

	"github.com/cgoncalveslck/go-api-ebening/bot"
)

func main() {
	fmt.Println("MONKE BOT🐒🤖")
	bot.BotToken = "MTI0NzY4NjEwMDUyOTU4MjIwMw.Gb6ub3.EuclP8a9MG7ehliWSh3MBP2fhl2r4Qt33kjKXA"
	bot.Run() // call the run function of bot/bot.go
}
