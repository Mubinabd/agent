package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"agent/config"
	"agent/models"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDB(url string) {
	var err error

	DB, err = gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		log.Fatal("DB connect error:", err)
	}

	// auto table create
	err = DB.AutoMigrate(&models.Expense{})
	if err != nil {
		log.Fatal("migration error:", err)
	}
}

func main() {
	cfg := config.LoadConfig()

	InitDB(cfg.Database.URL)

	bot, err := tgbotapi.NewBotAPI(cfg.Bot.Token)
	if err != nil {
		log.Fatal(err)
	}

	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Xarajat kiritish"},
		{Command: "daily", Description: "Bugungi statistika"},
		{Command: "weekly", Description: "Shu hafta"},
		{Command: "monthly", Description: "Shu oy"},
		{Command: "lastweek", Description: "O‘tgan hafta"},
		{Command: "lastmonth", Description: "O‘tgan oy"},
		{Command: "lastyear", Description: "O‘tgan yil"},
	}

	cmdCfg := tgbotapi.NewSetMyCommands(commands...)
	_, err = bot.Request(cmdCfg)
	if err != nil {
		log.Fatal(err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID
		text := update.Message.Text

		switch text {

		case "/start":
			bot.Send(tgbotapi.NewMessage(chatID,
				"Xarajat kiriting:\nMasalan: 15000 ovqat"))

		case "/daily":
			sendStats(bot, chatID, "day")

		case "/weekly":
			sendStats(bot, chatID, "week")

		case "/monthly":
			sendStats(bot, chatID, "month")

		case "/lastweek":
			sendStats(bot, chatID, "lastweek")

		case "/lastmonth":
			sendStats(bot, chatID, "lastmonth")

		case "/lastyear":
			sendStats(bot, chatID, "lastyear")

		default:
			ok, resp := saveExpense(chatID, text)
			bot.Send(tgbotapi.NewMessage(chatID, resp))

			if !ok {
				continue
			}
		}
	}
}

func saveExpense(userID int64, text string) (bool, string) {
	parts := strings.SplitN(text, " ", 2)

	if len(parts) < 2 {
		return false, "❌ Format: 15000 ovqat"
	}

	amount, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return false, "❌ summa noto‘g‘ri"
	}

	exp := models.Expense{
		UserID:      userID,
		Amount:      amount,
		Description: strings.TrimSpace(parts[1]),
	}

	if err := DB.Create(&exp).Error; err != nil {
		return false, "❌ DB error"
	}

	return true, "✅ Saqlandi"
}

func sendStats(bot *tgbotapi.BotAPI, userID int64, period string) {

	var expenses []models.Expense

	query := DB.Where("user_id = ?", userID)

	switch period {

	case "day":
		query = query.Where("created_at >= NOW() - interval '1 day'")

	case "week":
		query = query.Where("created_at >= NOW() - interval '7 days'")

	case "month":
		query = query.Where("created_at >= NOW() - interval '1 month'")

	case "lastweek":
		query = query.Where("created_at BETWEEN NOW() - interval '14 days' AND NOW() - interval '7 days'")

	case "lastmonth":
		query = query.Where("created_at BETWEEN NOW() - interval '2 month' AND NOW() - interval '1 month'")

	case "lastyear":
		query = query.Where("created_at >= NOW() - interval '1 year'")
	}

	err := query.Order("created_at desc").Find(&expenses).Error
	if err != nil {
		log.Println(err)
		return
	}

	if len(expenses) == 0 {
		bot.Send(tgbotapi.NewMessage(userID, "Hech qanday xarajat topilmadi"))
		return
	}

	var total float64
	var result strings.Builder

	for _, e := range expenses {
		total += e.Amount
		result.WriteString(fmt.Sprintf("%.0f - %s\n", e.Amount, e.Description))
	}

	result.WriteString("---------------------\n")
	result.WriteString(fmt.Sprintf("💰 JAMI: %.0f so'm", total))

	bot.Send(tgbotapi.NewMessage(userID, result.String()))
}
