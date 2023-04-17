package bulkai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ZYKJShadow/bulkai/pkg/ai"
	"github.com/ZYKJShadow/bulkai/pkg/ai/bluewillow"
	"github.com/ZYKJShadow/bulkai/pkg/ai/midjourney"
	"github.com/ZYKJShadow/bulkai/pkg/discord"
	"github.com/ZYKJShadow/bulkai/pkg/http"
	"github.com/ZYKJShadow/bulkai/pkg/img"
	"gopkg.in/yaml.v2"
)

type Album struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Status     string    `json:"status"`
	Percentage float32   `json:"percentage"`
	Images     []*Image  `json:"images"`
	Prompts    []string  `json:"prompts"`
	Finished   []int     `json:"finished"`
}

type Image struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
	File   string `json:"file"`
}

type Config struct {
	Debug       bool          `yaml:"debug"`
	Bot         string        `yaml:"bot"`
	Proxy       string        `yaml:"proxy"`
	Output      string        `yaml:"output"`
	Album       string        `yaml:"album"`
	Prefix      string        `yaml:"prefix"`
	Suffix      string        `yaml:"suffix"`
	Prompts     []string      `yaml:"prompts"`
	Variation   bool          `yaml:"variation"`
	Upscale     bool          `yaml:"upscale"`
	Download    bool          `yaml:"download"`
	Thumbnail   bool          `yaml:"thumbnail"`
	Channel     string        `yaml:"channel"`
	GuildID     string        `yaml:"groupID"`
	Concurrency int           `yaml:"concurrency"`
	Wait        time.Duration `yaml:"wait"`
	SessionFile string        `yaml:"session"`
	Session     Session       `yaml:"-"`
}

type Session struct {
	JA3             string `yaml:"ja3"`
	UserAgent       string `yaml:"user-agent"`
	Language        string `yaml:"language"`
	Token           string `yaml:"token"`
	SuperProperties string `yaml:"super-properties"`
	Locale          string `yaml:"locale"`
	Cookie          string `yaml:"cookie"`
}

type Container struct {
	Identify string
	InfoChan chan *ai.GenerateInfo
	Task     int
}

type MessageBroker struct {
	Containers map[string]*Container
}

type AiDrawClient struct {
	AiCli      ai.Client
	DiscordCli *discord.Client
	cfg        *Config
	sync.Mutex
	MessageBroker
}

func (a *AiDrawClient) DelContainer(identify string) {
	a.Lock()
	defer a.Unlock()
	delete(a.Containers, identify)
}

func (a *AiDrawClient) GetContainer(identify string) *Container {
	a.Lock()
	defer a.Unlock()
	return a.Containers[identify]
}

func (a *AiDrawClient) AddContainer(container *Container) {
	a.Lock()
	defer a.Unlock()
	a.Containers[container.Identify] = container
}

func CheckSessionInfo(cfg *Config) error {
	if cfg.Session.Token == "" {
		return errors.New("missing token")
	}
	if cfg.Bot == "" {
		return errors.New("missing bot name")
	}
	if cfg.Output == "" {
		return errors.New("missing output directory")
	}
	if cfg.Session.JA3 == "" {
		return errors.New("missing ja3")
	}
	if cfg.Session.UserAgent == "" {
		return errors.New("missing user agent")
	}
	if cfg.Session.Cookie == "" {
		return errors.New("missing cookie")
	}
	if cfg.Session.Language == "" {
		return errors.New("missing language")
	}
	return nil
}

func NewCli(ctx context.Context, cfg *Config) (drawClient *AiDrawClient, err error) {

	err = CheckSessionInfo(cfg)
	if err != nil {
		return
	}

	var newCli func(*discord.Client, string, string, bool) (ai.Client, error)

	switch strings.ToLower(cfg.Bot) {
	case "bluewillow":
		newCli = bluewillow.New
	case "midjourney":
		newCli = midjourney.New
	default:
		return nil, fmt.Errorf("unsupported bot: %s", cfg.Bot)
	}

	// Create http client
	httpClient, err := http.NewClient(cfg.Session.JA3, cfg.Session.UserAgent, cfg.Session.Language, cfg.Proxy)
	if err != nil {
		return nil, fmt.Errorf("couldn't create http client: %w", err)
	}

	// Set proxy
	if cfg.Proxy != "" {
		p := strings.TrimPrefix(cfg.Proxy, "http://")
		p = strings.TrimPrefix(p, "https://")
		_ = os.Setenv("HTTPS_PROXY", p)
		_ = os.Setenv("HTTP_PROXY", p)
	}

	if err := http.SetCookies(httpClient, "https://discord.com", cfg.Session.Cookie); err != nil {
		return nil, fmt.Errorf("couldn't set cookies: %w", err)
	}

	defer func() {
		cookie, err := http.GetCookies(httpClient, "https://discord.com")
		if err != nil {
			log.Printf("couldn't get cookies: %v\n", err)
		}
		cfg.Session.Cookie = strings.ReplaceAll(cookie, "\n", "")
		data, err := yaml.Marshal(cfg.Session)
		if err != nil {
			log.Println(fmt.Errorf("couldn't marshal session: %w", err))
		}
		if err := os.WriteFile(cfg.SessionFile, data, 0644); err != nil {
			log.Println(fmt.Errorf("couldn't write session: %w", err))
		}
	}()

	// discord client
	client, err := discord.New(&discord.Config{
		Token:           cfg.Session.Token,
		SuperProperties: cfg.Session.SuperProperties,
		Locale:          cfg.Session.Locale,
		UserAgent:       cfg.Session.UserAgent,
		HTTPClient:      httpClient,
		Debug:           cfg.Debug,
		Proxy:           cfg.Proxy,
	})

	if err != nil {
		return nil, fmt.Errorf("couldn't create discord client: %w", err)
	}

	// Start discord client
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("couldn't start discord client: %w", err)
	}

	cli, err := newCli(client, cfg.Channel, cfg.GuildID, cfg.Debug)
	if err != nil {
		return nil, fmt.Errorf("couldn't create %s client: %w", cfg.Bot, err)
	}
	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("couldn't start ai client: %w", err)
	}

	drawClient = &AiDrawClient{
		AiCli:      cli,
		DiscordCli: client,
		cfg:        cfg,
		MessageBroker: MessageBroker{
			Containers: make(map[string]*Container, 10),
		},
	}

	return
}

func (a *AiDrawClient) ReadImageChan(identify string) chan *ai.GenerateInfo {
	a.Lock()
	defer a.Unlock()
	container, ok := a.Containers[identify]
	if ok {
		return container.InfoChan
	}
	return nil
}

func (a *AiDrawClient) Generate(ctx context.Context, prompts []string, variation bool, upscale bool, identify string) error {

	var album *Album
	albumDir := fmt.Sprintf("%s/%s", a.cfg.Output, identify)
	imgDir := albumDir

	if album == nil {

		album = &Album{
			ID:        identify,
			Status:    "created",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
			Images:    []*Image{},
			Prompts:   prompts,
		}

		if err := os.MkdirAll(albumDir, 0755); err != nil {
			return fmt.Errorf("couldn't create album directory: %w", err)
		}

		if err := os.MkdirAll(imgDir, 0755); err != nil {
			return fmt.Errorf("couldn't create album images directory: %w", err)
		}

		log.Println("album created:", albumDir)

	}

	total := len(prompts) * 4
	if variation {
		total = total + total*4
	}

	container := a.GetContainer(identify)
	if container == nil {
		infoChan := make(chan *ai.GenerateInfo)
		container = &Container{
			Identify: identify,
			InfoChan: infoChan,
			Task:     1,
		}
		a.AddContainer(container)
	} else {
		container.Task++
	}

	ai.Bulk(ctx, a.AiCli, prompts, album.Finished, variation, upscale, a.cfg.Concurrency, container.InfoChan, a.cfg.Wait)
	
	log.Printf("album %s %s\n", albumDir, album.Status)
	return nil
}

func (a *AiDrawClient) ToImages(ctx context.Context, client *discord.Client, image *ai.Image, imgDir string, download, upscale, preview bool) []*Image {

	if !download {
		return []*Image{{
			Prompt: image.Prompt,
			URL:    image.URL,
		}}
	}

	// Create image output name
	localFile := image.FileName()
	imgOutput := fmt.Sprintf("%s/%s", imgDir, localFile)
	if err := client.Download(ctx, image.URL, imgOutput); err != nil {
		log.Println(fmt.Errorf("❌ couldn't download `%s`: %w", image.URL, err))
	}

	if upscale && preview {
		base := filepath.Base(imgOutput)
		base = base[:len(base)-len(filepath.Ext(base))]
		previewOutput := fmt.Sprintf("%s/_thumbnails/%s.jpg", imgDir, base)
		if err := img.Resize(8, imgOutput, previewOutput); err != nil {
			log.Println(fmt.Errorf("❌ couldn't preview `%s`: %w", imgOutput, err))
		}
	}

	if upscale {
		return []*Image{{
			Prompt: image.Prompt,
			URL:    image.URL,
			File:   localFile,
		}}
	}

	var images []*Image

	localFiles := image.FileNames()
	var imgOutputs []string
	for _, localFile := range localFiles {
		imgOutputs = append(imgOutputs, fmt.Sprintf("%s/%s", imgDir, localFile))
		images = append(images, &Image{
			Prompt: image.Prompt,
			URL:    image.URL,
			File:   localFile,
		})
	}
	if err := img.Split4(imgOutput, imgOutputs); err != nil {
		log.Println(fmt.Errorf("❌ couldn't split `%s`: %w", imgOutput, err))
		return images
	}

	if preview {
		for _, imgOutput := range imgOutputs {
			base := filepath.Base(imgOutput)
			base = base[:len(base)-len(filepath.Ext(base))]
			previewOutput := fmt.Sprintf("%s/%s.jpg", imgDir, base)
			if err := img.Resize(4, imgOutput, previewOutput); err != nil {
				log.Println(fmt.Errorf("❌ couldn't preview `%s`: %w", imgOutput, err))
			}
		}
	}
	return images
}
