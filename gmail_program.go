package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

var (
	mailboxes = []string{"example1@gmail.com", "example2@gmail.com"}
	senders   = map[string]struct{}{
		"sender1@gmail.com": {},
		"sender2@gmail.com": {},
	}
	lastProcessedMessageID sync.Map
	lastCheck              = time.Now().Unix() // Начальная отметка времени
)

const (
	token  = "" // Токен бота
	chatID = "" // ID чата
	apiURL = "https://api.telegram.org/bot%s/sendMessage"
)

// Отправка сообщения через ТГ бота
func send_msg(msg string) error {
	url := fmt.Sprintf(apiURL, token)
	request := map[string]string{
		"chat_id": chatID,
		"text":    msg,
	}
	jsonData, _ := json.Marshal(request)

	// Отправляем HTTP запрос
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// Загружаем токен из файла
func GetToken(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func GetTokenFromWeb(config *oauth2.Config, email string) *oauth2.Token {
	// Генерация URL для авторизации
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	openBrowser(authURL) // открытие ссылки на авторизацию

	fmt.Print("Введите код авторизации: ")
	var authCode string
	_, err := fmt.Scan(&authCode)
	if err != nil {
		log.Fatalf("Ошибка ввода кода: %v", err)
	}

	// Обмен кода на токен
	token, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		log.Fatalf("Не удалось получить токен: %v", err)
	}
	return token
}

func SaveToken(path string, token *oauth2.Token) {
	if err := os.MkdirAll("tokens", 0755); err != nil {
		log.Fatalf("Не удалось создать директорию tokens: %v", err)
	}

	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Ошибка создания файла токена: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

// Сохраняем токен в файл
func SaveToken_refr(file string, token *oauth2.Token) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// Обновление Access Token через Refresh Token
func RefreshAccessToken(config *oauth2.Config, token *oauth2.Token, file string) (*oauth2.Token, error) {
	if !token.Valid() {
		newToken, err := config.TokenSource(context.Background(), token).Token()
		if err != nil {
			return nil, fmt.Errorf("не удалось обновить токен: %v", err)
		}

		// Сохраняем обновлённый токен в файл
		err = SaveToken_refr(file, newToken)
		if err != nil {
			return nil, fmt.Errorf("не удалось сохранить токен: %v", err)
		}

		log.Println("Токен успешно обновлён")
		return newToken, nil
	}

	return token, nil
}

func openBrowser(url string) {
	var cmd string
	var args []string

	cmd = "rundll32"
	args = []string{"url.dll,FileProtocolHandler", url}

	exec.Command(cmd, args...).Start()
}

func ProcessMailbox(email string, srv *gmail.Service) {
	re := regexp.MustCompile(`<([^>]+)>`) // регулярное выражение "все что в скобках <>"

	for {
		query := fmt.Sprintf("after:%d", lastCheck) // Запрашиваем сообщения после lastCheck
		msgs, err := srv.Users.Messages.List(email).Q(query).MaxResults(10).Do()
		if err != nil {
			log.Fatalf("Ошибка при получении писем: %v", err)
		}

		if len(msgs.Messages) == 0 {
			fmt.Printf("[%s] Нет новых писем\n", email)
			time.Sleep(1 * time.Minute)
			continue
		}

		for _, m := range msgs.Messages {
			//Запрос заголовка письма
			msg, err := srv.Users.Messages.Get(email, m.Id).Format("metadata").
				MetadataHeaders("Subject", "From", "Date").Do()
			if err != nil {
				log.Printf("[%s] Ошибка загрузки письма %s: %v", email, m.Id, err)
				continue
			}
			//разбираем заголовок
			var subject, from, date string
			for _, h := range msg.Payload.Headers {
				switch h.Name {
				case "Subject":
					subject = h.Value
				case "From":
					from = h.Value
				case "Date":
					date = h.Value
				}
			}
			//
			matches := re.FindStringSubmatch(from)
			var senderEmail string
			if len(matches) >= 2 {
				senderEmail = matches[1]
			} else {
				senderEmail = from
			}

			if _, ok := senders[senderEmail]; ok { // Проверяем отправителя
				message := fmt.Sprintf("Новое письмо в %s | Тема: %s | От: %s | Дата: %s",
					email, subject, senderEmail, date)

				err := send_msg(message) // Отправляем сообщение в боте
				if err != nil {
					fmt.Println("Ошибка отправки:", err)
				} else {
					fmt.Println("Сообщение отправлено:", message)
				}
			}
		}

		// Обновляем lastCheck после обработки писем
		lastCheck = time.Now().Unix()

		time.Sleep(1 * time.Minute)
	}
}
func main() {
	ctx := context.Background()

	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Ошибка чтения credentials.json: %v", err)
	}

	config, err := google.ConfigFromJSON(b, "https://www.googleapis.com/auth/gmail.readonly")
	if err != nil {
		log.Fatalf("Ошибка создания конфига OAuth2: %v", err)
	}

	for _, email := range mailboxes { // Авторизация для каждой почты
		fmt.Printf("Авторизация для %s...\n", email)
		token := GetTokenFromWeb(config, email)
		SaveToken(fmt.Sprintf("tokens/%s.json", email), token)
		fmt.Printf("Токен для %s сохранен.\n", email)
	}

	for _, email := range mailboxes {
		tokenFile := fmt.Sprintf("tokens/%s.json", email)
		token, err := GetToken(tokenFile)
		if err != nil {
			log.Fatalf("Ошибка загрузки токена для %s: %v", email, err)
		}

		config := &oauth2.Config{
			ClientID:     "your-client-id",
			ClientSecret: "your-client-secret",
			RedirectURL:  "http://localhost:8080/",
			Scopes:       []string{"https://www.googleapis.com/auth/gmail.readonly"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/auth",
				TokenURL: "https://oauth2.googleapis.com/token",
			},
		}

		// Обновляем токен, если истёк
		token, err = RefreshAccessToken(config, token, tokenFile)
		if err != nil {
			log.Fatalf("Ошибка обновления токена для %s: %v", email, err)
		}
		// Создаем серис gmail
		client := config.Client(ctx, token)
		srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
		if err != nil {
			log.Fatalf("Не удалось создать Gmail сервис для %s: %v", email, err)
		}

		go ProcessMailbox(email, srv) // Обработка письма
	}

	select {}
}
