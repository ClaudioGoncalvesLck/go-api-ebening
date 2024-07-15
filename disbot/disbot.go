package disbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/ffmpeg-audio"
	"github.com/disgoorg/snowflake/v2"
	"github.com/jonas747/dca"
)

var BotToken string
var store Store

type Command string

// [SoundName]
type SoundList map[string]*Sound

// [UserID]
type Entrances map[string]*Sound

type Sound struct {
	MessageID snowflake.ID `json:"messageId"`
	URL       string       `json:"url"`
	Volume    int          `json:"volume"`
	// dca uses 0-256 for some reason, try mapping it to 0-100 for better UX // change this to uint8
}

type Channels struct {
	VoiceChannels []VoiceChannel `json:"voiceChannels"`
}

type State struct {
	SoundList       SoundList `json:"soundList"`
	Entrances       Entrances `json:"entrances"`
	Channels        Channels  `json:"channels"`
	SoundsChannel   snowflake.ID
	CommandsChannel snowflake.ID
}

// [guildID]
type Store map[snowflake.ID]*State

type VoiceChannel struct {
	ID             string
	GuildID        string
	Name           string
	UsersConnected []discord.User
}

var (
	stopPlayback  = make(chan bool)
	playbackMutex sync.Mutex
)

const (
	SoundsChannel   string = "sounds"
	CommandsChannel string = "bot-commands"
)

const (
	PlaySound   Command = ",s"
	SkipSound   Command = ",ss"
	Connect     Command = ",c"
	Help        Command = ",help"
	List        Command = ",list"
	Rename      Command = ",rename"
	AddEntrance Command = ",addentrance"
	Adjustvol   Command = ",adjustvol"
	Find        Command = ",f"
)

func Run() {
	// log.SetFlags(log.LstdFlags | log.Llongfile)
	// lvl := new(slog.LevelVar)

	// ss := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	// 	Level:     lvl,
	// 	AddSource: true,
	// }))
	// slog.SetDefault(ss)

	// lvl.Set(slog.LevelDebug)

	client, err := disgo.New(BotToken,
		bot.WithGatewayConfigOpts(
			// gateway.WithEnableResumeURL(true),
			// gateway.WithAutoReconnect(true),
			gateway.WithIntents(
				// gateway.IntentGuilds,
				// gateway.IntentGuildMessages,
				// gateway.IntentMessageContent,
				// gateway.IntentGuildVoiceStates,
				// gateway.IntentsGuild,
				// gateway.IntentGuildMembers,
				gateway.IntentsAll,
			),
		),
		// change flags
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagsAll)),
		bot.WithEventListenerFunc(messageHandler),
		bot.WithEventListenerFunc(readyHandler),
		bot.WithEventListenerFunc(voiceStateUpdate),
	)
	if err != nil {
		panic(err)
	}

	if err = client.OpenGateway(context.TODO()); err != nil {
		panic(err)
	}

	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM)
	<-s
}

func messageHandler(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	s := store[*e.GuildID]

	// if e.ChannelID == s.SoundsChannel {
	// 	handleSoundsChannel()
	// }

	if e.ChannelID == s.CommandsChannel {
		if len(e.Message.Attachments) > 0 {
			sendMessage(e, "If you're tring to upload a sound, put it in the 'sounds' channel")
			return
		}

		command := strings.Split(e.Message.Content, " ")[0]
		switch command {
		case string(Help):
			formattedMessage :=
				"### To add sounds, just send them to the 'sounds' channel as a message (just the file, no text)\n" +
					"**Commands:**\n" +
					"`,s <sound-name>` Plays a sound\n" +
					"`,connect` Connects to the voice channel you are in.\n" +
					"`,list` Lists all sounds in the sounds channel.\n" +
					"`,ss` Stops the current sound.\n" +
					"`,rename <current-name> <new-name>` Renames a sound.\n" +
					"`,addentrance <sound-name>` Sets a sound as your entrance sound.\n" +
					"`,adjustvol <sound-name> <volume>` Adjusts the volume of a sound (0-512).\n" +
					"`,f <sound-name>` Finds a sound by name and returns a link to it."

			sendMessage(e, formattedMessage)

		case string(List):
			listOutput := "```(" + fmt.Sprint(len(s.SoundList)) + ") " + "Available sounds :\n------------------\n\n"
			nb := 0
			for name := range s.SoundList {
				nb += 1
				var soundName = string(name)
				for len(soundName) < 15 {
					soundName += " "
				}
				listOutput += soundName + "\t"
				if nb%6 == 0 {
					listOutput += "\n"
				}
				// Discord max message length is 2000
				if len(listOutput) > 1950 { // removed condition for max sounds printed
					listOutput += "```"
					sendMessage(e, listOutput)
					listOutput = "```"
				}
			}
			listOutput += "```"
			if listOutput != "``````" {
				sendMessage(e, listOutput)
			}

		case string(Connect):
			c := e.Client().VoiceManager().GetConn(*e.GuildID)
			vChannel, err := getUserVChannel(e)
			if err != nil {
				if err.Error() == "user not in channel" {
					sendMessage(e, "You need to be in a voice channel")
					return
				} else {
					panic(err)
				}
			}

			if c == nil {
				go connectVoiceChannel(e, vChannel)
			}

		case string(PlaySound):
			if len(s.SoundList) == 0 {
				sendMessage(e, "No sounds loaded")
				return
			}

			//lookup sound locally only, upload or bootup should assure it's either here or nowhere
			mSplit := strings.Split(e.Message.Content, " ")
			if len(mSplit) != 2 {
				// TODO: handle this properly and call .help
				return
			}

			searchTerm := mSplit[1]
			sound, ok := s.SoundList[searchTerm]
			if !ok {
				sendMessage(e, "sound not found")
				return
			}

			conn := e.Client().VoiceManager().GetConn(*e.GuildID)
			fmt.Printf("get conn: %+v", conn)
			if conn == nil {
				sendMessage(e, "not in vc")
				return
			}

			go playSound(conn, sound.URL)
		}
	}
}

func getUserVChannel(e *events.MessageCreate) (snowflake.ID, error) {
	c := e.Client()
	allChan, err := c.Rest().GetGuildChannels(*e.GuildID)
	if err != nil {
		return 0, err
	}

	caches := c.Caches()
	ncErr := errors.New("user not in channel")
	for _, ch := range allChan {
		if ch.Type() == discord.ChannelTypeGuildVoice {
			vc, ex := caches.GuildAudioChannel(ch.ID())
			if !ex {
				continue
			}

			members := caches.AudioChannelMembers(vc)
			if len(members) == 0 {
				continue
			}

			for _, m := range members {
				if e.Message.Author.ID == m.User.ID {
					return ch.ID(), nil
				}
			}
		}
	}
	return 0, ncErr
}

func playSound(conn voice.Conn, url string) {
	session, err := dca.EncodeFile(url, dca.StdEncodeOptions)
	if err != nil {
		panic(err)
	}

	if err := conn.SetSpeaking(context.Background(), voice.SpeakingFlagMicrophone); err != nil {
		panic("error setting speaking flag: " + err.Error())
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	for range ticker.C {
		frame, err := session.OpusFrame()
		if err != nil {
			if err.Error() == "EOF" {
				session.Cleanup()
				if err := conn.SetSpeaking(context.Background(), voice.SpeakingFlagNone); err != nil {
					panic("error setting speaking flag: " + err.Error())
				}
				return
			} else {
				panic(err)
			}
		}
		conn.UDP().Write(frame)
	}
}

func playSoundFFMPEGPAUDIO(ctx context.Context, conn voice.Conn, url string) {
	if err := conn.SetSpeaking(ctx, voice.SpeakingFlagMicrophone); err != nil {
		panic("error setting speaking flag: " + err.Error())
	}

	response, err := http.Get(url)
	if err != nil {
		fmt.Println("Error fetching URL:", err)
		return
	}

	xx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opusProvider, err := ffmpeg.New(xx, response.Body)
	if err != nil {
		panic("error creating opus provider: " + err.Error())
	}

	conn.SetOpusFrameProvider(opusProvider)
	_ = opusProvider.Wait()
}

func sendMessage(e *events.MessageCreate, msg string) {
	e.Client().Rest().CreateMessage(e.ChannelID, discord.NewMessageCreateBuilder().SetContent(msg).Build())
}

func connectVoiceChannel(e *events.MessageCreate, vChannel snowflake.ID) {
	conn := e.Client().VoiceManager().CreateConn(*e.GuildID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	if err := conn.Open(ctx, vChannel, false, false); err != nil {
		panic("error connecting to voice channel: " + err.Error())
	}
}

func readyHandler(e *events.Ready) {
	c := e.Client()
	for _, guild := range e.Guilds {
		if store == nil {
			store = make(Store)
		}

		SoundsChannel, CommandsChannel, err := getRequiredChannels(c, guild.ID)
		if err != nil {
			panic(err)
		}

		gState := &State{
			Entrances: make(Entrances),
			Channels: Channels{
				VoiceChannels: []VoiceChannel{},
			},
			SoundList:       make(SoundList),
			SoundsChannel:   SoundsChannel,
			CommandsChannel: CommandsChannel,
		}

		store[guild.ID] = gState

		go getSounds(e, gState, guild.ID, 0)
	}
}

func getSounds(e *events.Ready, guildState *State, guildID snowflake.ID, beforeID snowflake.ID) {
	soundsChannelID := guildState.SoundsChannel
	channelMessages, err := e.Client().Rest().GetMessages(soundsChannelID, 0, beforeID, 0, 100)
	if err != nil {
		log.Printf("Failed to get messages: %v", err)
		return
	}

	for _, channelMessage := range channelMessages {
		if len(channelMessage.Attachments) > 0 {
			fileName := channelMessage.Attachments[0].Filename
			if strings.Split(fileName, ".")[1] != "mp3" {
				continue
			}
			trimmedName := strings.TrimSuffix(fileName, ".mp3")

			sound := &Sound{
				MessageID: channelMessage.ID,
				URL:       channelMessage.Attachments[0].URL,
			}
			guildState.SoundList[trimmedName] = sound

			if channelMessage.Content != "" {
				messageTags := strings.Split(channelMessage.Content, ";")
				handleSoundTags(messageTags, sound, guildState)
			}
		}
	}

	if len(channelMessages) == 100 {
		lastMessageID := channelMessages[len(channelMessages)-1].ID
		go getSounds(e, guildState, guildID, lastMessageID)
	}
}

func voiceStateUpdate(e *events.GuildVoiceLeave) {
	if e.Member.User.Bot {
		conn := e.Client().VoiceManager().GetConn(e.Member.GuildID)
		if conn != nil {
			fmt.Printf("closed: %+v", conn)
			conn.Close(context.Background())
		}
	}
}

func getRequiredChannels(c bot.Client, guildID snowflake.ID) (snowflake.ID, snowflake.ID, error) {
	var sC snowflake.ID
	var cC snowflake.ID

	channels, err := c.Rest().GetGuildChannels(guildID)
	if err != nil {
		return 0, 0, err
	}

	for _, channel := range channels {
		if channel.Name() == SoundsChannel {
			sC = channel.ID()
		}

		if channel.Name() == CommandsChannel {
			cC = channel.ID()
		}

		if sC != 0 && cC != 0 {
			return sC, cC, nil
		}
	}

	//un-hardcode this
	return 0, 0, errors.New("server needs 'sounds' and 'bot-commands' channels")
}

func handleSoundTags(tags []string, sound *Sound, guildState *State) {
	for _, tag := range tags {
		if tag == "" {
			continue
		}

		tagParts := strings.Split(tag, ":")
		tagType, tagValue := tagParts[0], tagParts[1]

		switch tagType {
		case "e":
			guildState.Entrances[tagValue] = sound
		case "v":
			volInt, err := strconv.ParseInt(tagValue, 10, 64)
			if err != nil {
				panic(err)
			}
			sound.Volume = int(volInt)
		}
	}
}
