package bulkai

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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

type Status struct {
	Percentage float32
	Estimated  time.Duration
	Err        error
}

type Option func(*option)

type option struct {
	onUpdate func(Status)
}

func WithOnUpdate(onUpdate func(Status)) Option {
	return func(o *option) {
		o.onUpdate = onUpdate
	}
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

// Generate launches multiple ai generations.
func Generate(ctx context.Context, cfg *Config, opts ...Option) error {

	err := CheckSessionInfo(cfg)
	if err != nil {
		return err
	}

	// Load options
	o := &option{}
	for _, opt := range opts {
		opt(o)
	}

	var newCli func(*discord.Client, string, string, bool) (ai.Client, error)
	var cli ai.Client
	switch strings.ToLower(cfg.Bot) {
	case "bluewillow":
		newCli = bluewillow.New
	case "midjourney":
		newCli = midjourney.New
	default:
		return fmt.Errorf("unsupported bot: %s", cfg.Bot)
	}

	// New album
	albumID := cfg.Album
	if albumID == "" {
		albumID = time.Now().UTC().Format("20060102_150405")
	}
	var album *Album
	albumDir := fmt.Sprintf("%s/%s", cfg.Output, albumID)
	imgDir := albumDir

	var prompts []string

	if len(prompts) == 0 {
		if len(cfg.Prompts) == 0 {
			return errors.New("missing prompt")
		}

		// Build prompts
		for _, prompt := range cfg.Prompts {
			// Check if prompt is a file
			if _, err := os.Stat(prompt); err != nil {
				prompts = append(prompts, prompt)
				continue
			}
			// Read lines from file
			file, err := os.Open(prompt)
			if err != nil {
				return fmt.Errorf("couldn't open prompt file: %w", err)
			}
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				prompt := strings.TrimSpace(scanner.Text())
				if prompt == "" {
					continue
				}
				prompts = append(prompts, prompt)
			}
			_ = file.Close()
			// Check for errors
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("couldn't read prompt file: %w", err)
			}
		}

		for i, prompt := range prompts {
			prompts[i] = fmt.Sprintf("%s%s%s", cfg.Prefix, prompt, cfg.Suffix)
		}
		sort.Strings(prompts)
	}

	// Check total images
	var lck sync.Mutex
	total := len(prompts) * 4
	if cfg.Variation {
		total = total + total*4
	}

	// Create http client
	httpClient, err := http.NewClient(cfg.Session.JA3, cfg.Session.UserAgent, cfg.Session.Language, cfg.Proxy)
	if err != nil {
		return fmt.Errorf("couldn't create http client: %w", err)
	}

	// Set proxy
	if cfg.Proxy != "" {
		p := strings.TrimPrefix(cfg.Proxy, "http://")
		p = strings.TrimPrefix(p, "https://")
		os.Setenv("HTTPS_PROXY", p)
		os.Setenv("HTTP_PROXY", p)
	}

	if err := http.SetCookies(httpClient, "https://discord.com", cfg.Session.Cookie); err != nil {
		return fmt.Errorf("couldn't set cookies: %w", err)
	}
	defer func() {
		cookie, err := http.GetCookies(httpClient, "https://discord.com")
		if err != nil {
			log.Printf("couldn't get cookies: %v\n", err)
		}
		cfg.Session.Cookie = strings.ReplaceAll(cookie, "\n", "")
		// TODO: save session to common method
		data, err := yaml.Marshal(cfg.Session)
		if err != nil {
			log.Println(fmt.Errorf("couldn't marshal session: %w", err))
		}
		if err := os.WriteFile(cfg.SessionFile, data, 0644); err != nil {
			log.Println(fmt.Errorf("couldn't write session: %w", err))
		}
	}()

	// Create discord client
	client, err := discord.New(ctx, &discord.Config{
		Token:           cfg.Session.Token,
		SuperProperties: cfg.Session.SuperProperties,
		Locale:          cfg.Session.Locale,
		UserAgent:       cfg.Session.UserAgent,
		HTTPClient:      httpClient,
		Debug:           cfg.Debug,
		Proxy:           cfg.Proxy,
	})
	if err != nil {
		return fmt.Errorf("couldn't create discord client: %w", err)
	}

	// Start discord client
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("couldn't start discord client: %w", err)
	}

	cli, err = newCli(client, cfg.Channel, cfg.GuildID, cfg.Debug)
	if err != nil {
		return fmt.Errorf("couldn't create %s client: %w", cfg.Bot, err)
	}
	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("couldn't start ai client: %w", err)
	}

	// Album doesn't exist, create it
	if album == nil {
		album = &Album{
			ID:        albumID,
			Status:    "created",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
			Images:    []*Image{},
			Prompts:   prompts,
		}
		if err := os.MkdirAll(albumDir, 0755); err != nil {
			return fmt.Errorf("couldn't create album directory: %w", err)
		}
		if cfg.Download {
			if err := os.MkdirAll(imgDir, 0755); err != nil {
				return fmt.Errorf("couldn't create album images directory: %w", err)
			}
		}
		log.Println("album created:", albumDir)
	}

	imageChan := ai.Bulk(ctx, cli, prompts, album.Finished, cfg.Variation, cfg.Upscale, cfg.Concurrency, cfg.Wait)
	var exit bool
	for !exit {
		var status string
		select {
		case <-ctx.Done():
			status = "cancelled"
			exit = true
		case image, ok := <-imageChan:
			if !ok {
				status = "finished"
				if album.Percentage < 100 {
					status = "partially finished"
				}
				exit = true
			} else {
				status = "running"
				lck.Lock()
				album.UpdatedAt = time.Now().UTC()
				images := toImages(ctx, client, image, imgDir, cfg.Download, cfg.Upscale, cfg.Thumbnail)
				if len(images) > 0 {
					album.Images = append(album.Images, images...)
					if image.IsLast {
						album.Finished = append(album.Finished, image.PromptIndex)
					}
				}
				lck.Unlock()
			}
			//default:
			//	err = errors.New("今日AI绘图已超出使用上限，请稍后再试")
			//	o.onUpdate(Status{
			//		Err: err,
			//	})
			//	return err
		}
		lck.Lock()
		album.UpdatedAt = time.Now().UTC()
		percentage := float32(len(album.Images)) * 100.0 / float32(total)
		if percentage > album.Percentage {
			avg := album.UpdatedAt.Sub(album.CreatedAt) / time.Duration(len(album.Images))
			estimated := (time.Duration(total-len(album.Images)) * avg).Round(time.Minute)
			if o.onUpdate != nil {
				o.onUpdate(Status{
					Percentage: percentage,
					Estimated:  estimated,
				})
			}
		}
		album.Percentage = percentage
		album.Status = status
		lck.Unlock()
	}
	log.Printf("album %s %s\n", albumDir, album.Status)

	return nil
}

func toImages(ctx context.Context, client *discord.Client, image *ai.Image, imgDir string, download, upscale, preview bool) []*Image {
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

	// Generate preview image
	if upscale && preview {
		base := filepath.Base(imgOutput)
		base = base[:len(base)-len(filepath.Ext(base))]
		previewOutput := fmt.Sprintf("%s/_thumbnails/%s.jpg", imgDir, base)
		if err := img.Resize(8, imgOutput, previewOutput); err != nil {
			log.Println(fmt.Errorf("❌ couldn't preview `%s`: %w", imgOutput, err))
		}
	}

	// Current image is an upscale image, return it
	if upscale {
		return []*Image{{
			Prompt: image.Prompt,
			URL:    image.URL,
			File:   localFile,
		}}
	}

	var images []*Image

	// Split preview images when upscale is disabled
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

	// Create preview images
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
