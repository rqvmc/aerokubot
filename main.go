package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

var (
	telegramToken   string
	webHookHostname string
	listen          string
	githubToken     string
	version         bool
	debug           bool
	gitRevision     = "HEAD"
	buildStamp      = "unknown"
)

func init() {
	flag.StringVar(&telegramToken, "token", "", "Telegram bot token (required)")
	flag.StringVar(&webHookHostname, "webhook-hostname", "localhost", "Telegram webhook hostname (required)")
	flag.StringVar(&listen, "listen", ":9090", "Telegram listen host and port")
	flag.StringVar(&githubToken, "github-token", "", "GitHub token with public read permissions")
	flag.BoolVar(&version, "version", false, "Show version and exit")
	flag.BoolVar(&debug, "debug", false, "Run in debug mode (will print all req/resp)")
	flag.Parse()

	if flag.NFlag() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	if version {
		showVersion()
		os.Exit(0)
	}
}

func showVersion() {
	fmt.Printf("Git Revision: %s\n", gitRevision)
	fmt.Printf("UTC Build Time: %s\n", buildStamp)
}

const (
	welcome = `
Welcome to Aerokube chat! We can help in English 🇬🇧, так же как и по-русски 🇷🇺!

Having troubles? Please provide your environment and Aerokube tools versions!

Есть проблемы? Начни вопрос с окружения и используемой версии инструментов Aerokube!
`

	releasesCommand = "releases"
)

type gql struct {
	Query string `json:"query"`
}

type result struct {
	Data map[string]repo `json:"data"`
}

type repo struct {
	Releases struct {
		Nodes []release `json:"nodes"`
	} `json:"releases"`
}

type release struct {
	Url         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	Tag         struct {
		Name string `json:"name"`
	} `json:"tag"`
}

func main() {
	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Fatalf("[INIT] [Failed to init Telegram Bot API: %v]", err)
	}

	bot.Debug = debug

	log.Printf("[INIT] [Authorized on account %s, debug mode: %v]", bot.Self.UserName, debug)

	webHookUrl := fmt.Sprintf("https://%s/%s", webHookHostname, bot.Token)
	log.Printf("[INIT] [Webhook URL is %s]", webHookUrl)
	_, err = bot.SetWebhook(tgbotapi.NewWebhook(webHookUrl))
	if err != nil {
		log.Fatalf("[INIT] [Failed to set webhook: %v]", err)
	}

	info, err := bot.GetWebhookInfo()
	if err != nil {
		log.Fatalf("[INIT] [Failed to get webhook info: %v]", err)
	}
	log.Printf("[INIT] [Webhook info: url = %s, pending updates = %d]", info.URL, info.PendingUpdateCount)

	updates := bot.ListenForWebhook("/" + bot.Token)

	go func() {
		for update := range updates {

			log.Printf("%v", update)

			if update.Message == nil {
				if debug {
					log.Printf("[UNKNOWN_MESSAGE] [%v]", update)
				}
				continue
			}

			if update.Message.Chat.IsGroup() || update.Message.Chat.IsSuperGroup() {
				if update.Message.NewChatMembers != nil {
					var newUsers []string

					for _, user := range *update.Message.NewChatMembers {
						newUsers = append(newUsers, "@"+getUserName(user))
					}

					joinedUsers := strings.Join(newUsers, " ")

					msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Hey, %s\n%s", joinedUsers, welcome))
					send(bot, msg)
				}
			}

			// COMMANDS
			if update.Message.IsCommand() {

				switch update.Message.Command() {
				case releasesCommand:
					result := make(chan string)
					go releases(result)

					select {
					case msg := <-result:
						resp := tgbotapi.NewMessage(update.Message.Chat.ID, msg)
						resp.ReplyToMessageID = update.Message.MessageID
						resp.ParseMode = tgbotapi.ModeMarkdown
						send(bot, resp)
					case <-time.After(10 * time.Second):
					}
				}
			}
		}
	}()

	stop := make(chan os.Signal)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	e := make(chan error)
	go func() {
		e <- http.ListenAndServe(listen, nil)
	}()
	select {
	case err := <-e:
		log.Fatalf("[INIT] [Server error: %v]", err)
	case <-stop:
	}

}

func send(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) {
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("[FAILED_TO_SEND_MESSAGE] [%v]", msg)
	}
}

func getUserName(user tgbotapi.User) string {
	if user.UserName == "" {
		return user.FirstName
	}
	return user.UserName
}

func releases(msg chan<- string) {
	query := `
fragment release on Repository {
  releases(last: 1) {
    nodes {
      url
      publishedAt
      tag {
        name
      }
    }
  }
}

query repos {
  selenoid: repository(owner: "aerokube", name: "selenoid") {
    ...release
  }
  moon: repository(owner: "aerokube", name: "moon") {
    ...release
  }
  cm: repository(owner: "aerokube", name: "cm") {
    ...release
  }
  selenoid_ui: repository(owner: "aerokube", name: "selenoid-ui") {
    ...release
  }
  ggr: repository(owner: "aerokube", name: "ggr") {
    ...release
  }
  ggr_ui: repository(owner: "aerokube", name: "ggr-ui") {
    ...release
  }
}
`

	q, err := json.Marshal(gql{Query: query})
	if err != nil {
		log.Printf("[FETCH_RELEASES} [Failed to marshal GraphQL query: %v]", err)
		return
	}

	req, err := http.NewRequest(
		"POST",
		"https://api.github.com/graphql",
		bytes.NewReader(q),
	)
	if err != nil {
		log.Printf("[FETCH_RELEASES] [Failed to create Github request: %v]", err)
		return
	}
	req.Header.Add("Authorization", "Bearer "+githubToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[FETCH_RELEASES] [Failed to fetch Github releases for aerokube project: %v]", err)
		return
	}
	defer resp.Body.Close()

	result := &result{}

	err = json.NewDecoder(resp.Body).Decode(result)
	if err != nil {
		log.Printf("[FETCH_RELEASES] [Failed to unmarshal Github response: %v]", err)
		return
	}

	var repos []string

	var names []string
	for k := range result.Data {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		repo := result.Data[name]
		rel := repo.Releases.Nodes[0]

		repos = append(repos, fmt.Sprintf(
			"*%s*: [%s](%s) - %s",
			name,
			rel.Tag.Name,
			rel.Url,
			humanize.Time(rel.PublishedAt),
		))
	}

	msg <- strings.Join(repos, "\n")
}
