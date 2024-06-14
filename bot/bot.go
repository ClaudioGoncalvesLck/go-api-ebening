package bot

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

var BotToken string

var State BotState

type BotState struct {
	SoundList        map[string]string
	VoiceChannels    VoiceChannels
	VoiceConnections []*discordgo.VoiceConnection
}

type VoiceChannels struct {
	Channels []VoiceChannel
}
type VoiceChannel struct {
	ID             string
	GuildID        string
	Name           string
	UsersConnected []discordgo.User
}

var (
	playbackQueue = make(chan string, 10)
	stopPlayback  = make(chan bool)
	playbackMutex sync.Mutex
)

type Command string
type ChannelName string

const (
	SoundsChannel   ChannelName = "sounds"
	CommandsChannel ChannelName = "bot-commands"
)

const (
	PlaySound Command = ".s"
	SkipSound Command = ".ss"
	Connect   Command = ".c"
	Help      Command = ".help"
	List      Command = ".list"
)

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		panic(err)
	}

	State = BotState{
		// i dont think i need to do this
		SoundList: make(map[string]string),
		VoiceChannels: VoiceChannels{
			Channels: []VoiceChannel{},
		},
	}

	discord.AddHandler(voiceStateUpdate)
	discord.AddHandler(newMessage)

	// open session
	discord.Open()
	defer discord.Close() // close session, after function termination

	// keep bot running untill there is NO os interruption (ctrl + C)
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

// TODO:
// 	look into error handling in general
// 	look into discord ui for messages (commands and message components)
//  	https://discord.com/developers/docs/interactions/overview
// 	try improving zip upload
// 	implement renaming and deleting sounds
// 	implement setting a volume (maybe command gets a sound, bot reuploads with message like v:0-100)
// 	implement entrances

func newMessage(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if userMessage.Author.Bot {
		return
	}

	channel, err := discord.Channel(userMessage.ChannelID)
	if err != nil {
		panic(err)
	}

	if channel.Name == string(SoundsChannel) {
		handleSoundsChannel(discord, userMessage)
	}

	if channel.Name == string(CommandsChannel) {
		handleCommandsChannel(discord, userMessage)
	}

}

// sample from github.com/jonas747/dca
func PlayAudioFile(v *discordgo.VoiceConnection, path string) {
	playbackMutex.Lock()
	defer playbackMutex.Unlock()

	err := v.Speaking(true)
	if err != nil {
		log.Fatal("Failed setting speaking", err)
	}

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 128

	encodeSession, err := dca.EncodeFile(path, opts)
	if err != nil {
		log.Fatal("Failed creating an encoding session: ", err)
	}

	done := make(chan error)
	dca.NewStream(encodeSession, v, done)

	for {
		select {
		case err := <-done:
			if err == dca.ErrVoiceConnClosed {
				encodeSession.Cleanup()
			} else {
				if err != nil && err != io.EOF {
					log.Fatal("An error occurred", err)
				}
			}

			v.Speaking(false)
			encodeSession.Cleanup()
			return
		case <-stopPlayback:
			v.Speaking(false)
			encodeSession.Cleanup()
			return
		}
	}
}

func findSoundRecursive(d *discordgo.Session, userMessage *discordgo.MessageCreate, searchTerm, beforeID string) (*discordgo.Message, error) {
	soundsChannel := getSoundsChannelID(d, userMessage)
	channelMessages, err := d.ChannelMessages(soundsChannel, 100, beforeID, "", "")
	if err != nil {
		return nil, err
	}

	for _, channelMessage := range channelMessages {
		if len(channelMessage.Attachments) == 0 {
			continue
		}
		if strings.Split(channelMessage.Attachments[0].Filename, ".")[0] == searchTerm {
			return channelMessage, nil
		}
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil, errors.New("sound not found")
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return findSoundRecursive(d, userMessage, searchTerm, lastMessageID)
}

func tryConnectingToVoice(d *discordgo.Session, userMessage *discordgo.MessageCreate) (*discordgo.VoiceConnection, error) {
	voiceState, err := d.State.VoiceState(userMessage.GuildID, userMessage.Author.ID)
	if err != nil {
		if err.Error() != "state cache not found" {
			return nil, err
		} else {
			return nil, nil
		}
	}

	voice, err := d.ChannelVoiceJoin(userMessage.GuildID, voiceState.ChannelID, false, false)
	if err != nil {
		return nil, err
	}

	State.VoiceConnections = append(State.VoiceConnections, voice)
	return voice, nil
}

func downloadFile(filepath string, url string) (err error) {
	out, err := os.Create("sounds/" + filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func handleSoundsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		for _, attachment := range userMessage.Attachments {
			if strings.Split(attachment.Filename, ".")[1] == "zip" {
				handleZipUpload(discord, userMessage, attachment)
			}
		}
	} else {
		m, err := discord.ChannelMessageSendReply(userMessage.ChannelID, "Please use this channel for files only", userMessage.Reference())
		if err != nil {
			panic(err)
		}
		time.Sleep(3 * time.Second)
		discord.ChannelMessageDelete(userMessage.ChannelID, m.ID)
		discord.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
	}
}

func handleCommandsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		discord.ChannelMessageSendReply(userMessage.ChannelID, "If you're tring to upload a sound, put in in 'sounds' channel", userMessage.Reference())
	}

	// skips the current sound only if the message is exactly ".ss" to avoid accidental skips
	if userMessage.Content == string(SkipSound) {
		stopPlayback <- true
		return
	}

	command := strings.Split(userMessage.Content, " ")[0]
	switch {
	case command == string(Help):
		formattedMessage := "### **Note:** Always mention sounds by name without '.mp3'\n" +
			"**Commands:**\n" +
			"`.s (sound) <sound-name>`:\nPlays a sound or \n" +
			"`.c (connect)`:\nConnects to the voice channel you are in.\n" +
			"`.list`:\nLists all the sounds in the sounds channel." +
			"`.ss (skip sound)`:\nStops the current sound.\n"

		discord.ChannelMessageSend(userMessage.ChannelID, formattedMessage)

	case command == string(Connect):
		_, err := tryConnectingToVoice(discord, userMessage)
		if err != nil {
			panic(err)
		}

	case command == string(PlaySound):
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		soundEntryMessage, err := findSoundRecursive(discord, userMessage, searchTerm, "")
		if err != nil {
			if err.Error() == "sound not found" {
				discord.ChannelMessageSend(userMessage.ChannelID, "sound not found")
				return
			} else {
				panic(err)
			}
		}

		soundURL := soundEntryMessage.Attachments[0].URL
		voice, err := tryConnectingToVoice(discord, userMessage)
		if err != nil {
			panic(err)
		}
		if voice == nil {
			discord.ChannelMessageSend(userMessage.ChannelID, "You need to be in a voice channel")
			return
		}
		go PlaybackManager(voice)

		if command == string(PlaySound) {
			QueuePlayback(voice, soundURL)
			return
		}

	case command == string(List):
		if len(State.SoundList) == 0 {
			err := getSoundsRecursive(discord, userMessage, "")
			if err != nil {
				panic(err)
			}
		}

		// shoutout rasmussy
		listOutput := "```Available sounds :\n------------------\n\n"
		nb := 0
		for _, soundName := range State.SoundList {
			nb += 1
			for len(soundName) < 15 {
				soundName += " "
			}
			listOutput += soundName + "\t"
			if nb%6 == 0 {
				listOutput += "\n"
			}
			// Discord max message length is 2000
			if nb%42 == 0 || len(listOutput) > 1900 {
				listOutput += "```"
				discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
				listOutput = "```"
			}
		}
		listOutput += "```"
		if listOutput != "``````" {
			discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
		}
	}
}

// This is most likely not the best way to do this
// if message has a zip file, extract it and send every .mp3 file to the sounds channel
// delete the original message and the files written to disk
func handleZipUpload(d *discordgo.Session, userMessage *discordgo.MessageCreate, attachment *discordgo.MessageAttachment) {
	err := os.Mkdir("sounds", 0755)
	if err != nil {
		if !os.IsExist(err) {
			panic(err)
		}
	}

	filePath := filepath.Join("sounds", attachment.Filename)
	downloadFile(attachment.Filename, attachment.URL)

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		panic(err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if strings.Split(file.Name, ".")[1] == "mp3" {
			fileReader, err := file.Open()
			if err != nil {
				panic(err)
			}

			filePath := "sounds/" + file.Name
			fileWriter, err := os.Create(filePath)
			if err != nil {
				panic(err)
			}

			_, err = d.ChannelFileSend(userMessage.ChannelID, file.Name, fileReader)
			if err != nil {
				panic(err)
			}

			fileWriter.Close()
			fileReader.Close()
		}
	}

	err = os.RemoveAll("sounds")
	if err != nil {
		panic(err)
	}
	d.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
}

func getSoundsRecursive(d *discordgo.Session, userMessage *discordgo.MessageCreate, beforeID string) error {
	soundsChannelID := getSoundsChannelID(d, userMessage)
	if soundsChannelID == "" {
		return errors.New("sounds channel not found")
	}
	channelMessages, err := d.ChannelMessages(soundsChannelID, 100, beforeID, "", "")
	if err != nil {
		return err
	}

	for _, channelMessage := range channelMessages {
		State.SoundList[channelMessage.ID] = strings.Split(channelMessage.Attachments[0].Filename, ".")[0]
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return getSoundsRecursive(d, userMessage, lastMessageID)
}

func getSoundsChannelID(d *discordgo.Session, userMessage *discordgo.MessageCreate) string {
	channels, err := d.GuildChannels(userMessage.GuildID)
	if err != nil {
		panic(err)
	}

	for _, channel := range channels {
		if channel.Name == string(SoundsChannel) {
			return channel.ID
		}
	}

	return ""
}

func QueuePlayback(v *discordgo.VoiceConnection, path string) {
	select {
	case playbackQueue <- path:
	default:
		fmt.Println("Queue is full, discarding:", path)
	}
}

func PlaybackManager(v *discordgo.VoiceConnection) {
	for {
		select {
		case path := <-playbackQueue:
			PlayAudioFile(v, path)
		}
	}
}

// Redo this
func getUsersInVC(d *discordgo.Session, guildID string) VoiceChannels {
	currentGuild, err := d.State.Guild(guildID)
	if err != nil {
		fmt.Println("Error getting guild:", err)
		return VoiceChannels{}
	}

	voiceChannelsMap := make(map[string]*VoiceChannel)

	// Initialize the map with all voice channels in the guild
	for _, channel := range currentGuild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannelsMap[channel.ID] = &VoiceChannel{
				ID:             channel.ID,
				GuildID:        guildID,
				Name:           channel.Name,
				UsersConnected: []discordgo.User{},
			}
		}
	}

	// Populate users in each voice channel
	for _, vs := range currentGuild.VoiceStates {
		if vc, ok := voiceChannelsMap[vs.ChannelID]; ok {
			user, err := d.User(vs.UserID)
			if err != nil {
				fmt.Println("Error getting user:", err)
				continue
			}
			if !user.Bot {
				vc.UsersConnected = append(vc.UsersConnected, *user)
			}
		}
	}

	var voiceChannels VoiceChannels
	for _, vc := range voiceChannelsMap {
		voiceChannels.Channels = append(voiceChannels.Channels, *vc)
	}

	State.VoiceChannels = voiceChannels
	return voiceChannels
}

func voiceStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	// if there is no voice state, build it
	if v.Member.User.Bot {
		return
	}
	if len(State.VoiceChannels.Channels) == 0 {
		getUsersInVC(d, v.GuildID)
	} else {
		// if there is, update it with whatever happened
		if v.BeforeUpdate == nil {
			fmt.Println("User joined voice channel")
			voiceChannelStateUpdate(d, v.UserID, v.ChannelID)
		} else {
			if v.ChannelID != "" && v.BeforeUpdate != nil {
				fmt.Println("User switched voice channels")
				voiceChannelStateUpdate(d, v.UserID, v.ChannelID)
			} else {
				fmt.Println("User left voice channel")
				voiceChannelStateUpdate(d, v.UserID, "")
			}
		}
	}

	var usersInVC []string
	for _, vc := range State.VoiceChannels.Channels {
		for _, user := range vc.UsersConnected {
			usersInVC = append(usersInVC, user.Username)
		}

	}

	if len(usersInVC) == 0 {
		voiceConnections := State.VoiceConnections
		for _, vc := range voiceConnections {
			if vc.GuildID == v.GuildID {
				vc.Disconnect()
				for idx, vc := range State.VoiceConnections {
					if vc.ChannelID == v.ChannelID {
						State.VoiceConnections = append(State.VoiceConnections[:idx], State.VoiceConnections[idx+1:]...)
					}
				}
			}
		}
	}

	if len(usersInVC) == 1 {
		//	find channel with the user that connected and join it
		for _, vc := range State.VoiceChannels.Channels {
			for _, user := range vc.UsersConnected {
				if user.Username == usersInVC[0] {
					// abstract this to the tryConnectingToVoice function
					voice, err := d.ChannelVoiceJoin(v.GuildID, vc.ID, false, false)
					if err != nil {
						fmt.Println("Error joining voice channel:", err)
						return
					}
					State.VoiceConnections = append(State.VoiceConnections, voice)
				}
			}
		}
	}
}

func voiceChannelStateUpdate(d *discordgo.Session, userID string, channelID string) {
	// remove user from old channel
	for idx, vc := range State.VoiceChannels.Channels {
		for i, user := range vc.UsersConnected {
			var f bool
			if user.ID == userID {
				State.VoiceChannels.Channels[idx].UsersConnected = append(vc.UsersConnected[:i], vc.UsersConnected[i+1:]...)
				f = true
				break
			}
			if f { // ooga booga code
				break
			}
		}
	}

	// add user to new channel if he went to one
	if channelID != "" {
		for idx, vc := range State.VoiceChannels.Channels {
			if vc.ID == channelID {
				user, err := d.User(userID)
				if err != nil {
					fmt.Println("Error getting user:", err)
					return
				}
				State.VoiceChannels.Channels[idx].UsersConnected = append(vc.UsersConnected, *user)
				return // Assuming the channelID is unique and found
			}
		}
	}
}
