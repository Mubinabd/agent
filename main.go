package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"agent/models"
)

var DB *gorm.DB
var Bot *tgbotapi.BotAPI

func InitDB() {
	dsn := os.Getenv("DATABASE_URL")

	var db *gorm.DB
	var err error

	for i := 0; i < 10; i++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			break
		}
		log.Printf("DB ulanishda xato, qayta urinilmoqda... (%d/10)", i+1)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		log.Fatal("DB ga ulanib bo'lmadi:", err)
	}

	DB = db

	err = DB.AutoMigrate(&models.Expense{})
	if err != nil {
		log.Fatal("migration error:", err)
	}
}

func getAllUserIDs() []int64 {
	var userIDs []int64
	DB.Model(&models.Expense{}).Distinct("user_id").Pluck("user_id", &userIDs)
	return userIDs
}

// 1 kun ichida xarajat kiritilganmi tekshirish
func checkInactiveUsers() {
	log.Println("⏰ Scheduler: Faol bo'lmagan foydalanuvchilar tekshirilmoqda...")
	userIDs := getAllUserIDs()

	for _, userID := range userIDs {
		var count int64
		DB.Model(&models.Expense{}).
			Where("user_id = ? AND created_at >= NOW() - interval '1 day'", userID).
			Count(&count)

		if count == 0 {
			msg := "⚠️ Bugun hali xarajat kiritmagansiz!\nUnutmang, xarajatlaringizni kuzatib boring 📊"
			Bot.Send(tgbotapi.NewMessage(userID, msg))
			log.Printf("📨 Eslatma yuborildi: userID=%d", userID)
		}
	}
}

// 1 oylik xarajat 1 mlndan oshganmi tekshirish
func checkBudgetLimit(userID int64) {
	var total float64
	DB.Model(&models.Expense{}).
		Where("user_id = ? AND created_at >= date_trunc('month', NOW())", userID).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&total)

	if total > 1_000_000 {
		msg := fmt.Sprintf(
			"🚨 Diqqat! Bu oy xarajatingiz *1,000,000 so'mdan* oshdi!\n💸 Jami: *%.0f so'm*\n\nXarajatlarni kamaytiring!",
			total,
		)
		msgObj := tgbotapi.NewMessage(userID, msg)
		msgObj.ParseMode = "Markdown"
		Bot.Send(msgObj)
	}
}

// Haftalik hisobot yuborish
func sendWeeklyReports() {
	log.Println("📅 Scheduler: Haftalik hisobotlar yuborilmoqda...")
	userIDs := getAllUserIDs()

	for _, userID := range userIDs {
		var expenses []models.Expense
		DB.Where("user_id = ? AND created_at >= NOW() - interval '7 days'", userID).
			Order("created_at desc").
			Find(&expenses)

		if len(expenses) == 0 {
			Bot.Send(tgbotapi.NewMessage(userID, "📅 Haftalik hisobot: Bu hafta xarajat kiritilmagan."))
			continue
		}

		var total float64
		var result strings.Builder
		result.WriteString("📅 *Haftalik hisobot:*\n\n")

		for _, e := range expenses {
			total += e.Amount
			result.WriteString(fmt.Sprintf("• %.0f - %s\n", e.Amount, e.Description))
		}

		result.WriteString("\n─────────────────\n")
		result.WriteString(fmt.Sprintf("💰 *Jami: %.0f so'm*", total))

		msg := tgbotapi.NewMessage(userID, result.String())
		msg.ParseMode = "Markdown"
		Bot.Send(msg)
		log.Printf("📨 Haftalik hisobot yuborildi: userID=%d", userID)
	}
}

// Oylik hisobot yuborish
func sendMonthlyReports() {
	log.Println("📅 Scheduler: Oylik hisobotlar yuborilmoqda...")
	userIDs := getAllUserIDs()

	for _, userID := range userIDs {
		var expenses []models.Expense
		DB.Where("user_id = ? AND created_at >= date_trunc('month', NOW() - interval '1 month') AND created_at < date_trunc('month', NOW())", userID).
			Order("created_at desc").
			Find(&expenses)

		if len(expenses) == 0 {
			Bot.Send(tgbotapi.NewMessage(userID, "🗓 Oylik hisobot: O'tgan oy xarajat kiritilmagan."))
			continue
		}

		// Kategoriya bo'yicha guruhlash
		categoryMap := make(map[string]float64)
		var total float64

		for _, e := range expenses {
			categoryMap[e.Description] += e.Amount
			total += e.Amount
		}

		var result strings.Builder
		result.WriteString("🗓 *Oylik hisobot:*\n\n")

		for desc, sum := range categoryMap {
			percent := (sum / total) * 100
			result.WriteString(fmt.Sprintf("• %s: %.0f so'm (%.1f%%)\n", desc, sum, percent))
		}

		result.WriteString("\n─────────────────\n")
		result.WriteString(fmt.Sprintf("💰 *Jami: %.0f so'm*\n", total))
		result.WriteString(fmt.Sprintf("📊 *Jami xarajatlar soni: %d*", len(expenses)))

		msg := tgbotapi.NewMessage(userID, result.String())
		msg.ParseMode = "Markdown"
		Bot.Send(msg)
		log.Printf("📨 Oylik hisobot yuborildi: userID=%d", userID)
	}
}

// Schedulerni ishga tushirish
func startScheduler(loc *time.Location) {
	go func() {
		for {
			now := time.Now().In(loc)

			// --- Kunlik eslatma: har kuni kechqurun 21:00 da ---
			nextCheck := time.Date(now.Year(), now.Month(), now.Day(), 21, 0, 0, 0, loc)
			if now.After(nextCheck) {
				nextCheck = nextCheck.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(nextCheck))
			checkInactiveUsers()

			// --- Haftalik hisobot: har dushanba 09:00 da ---
			go func() {
				for {
					now := time.Now().In(loc)
					daysUntilMonday := (int(time.Monday) - int(now.Weekday()) + 7) % 7
					if daysUntilMonday == 0 && now.Hour() >= 9 {
						daysUntilMonday = 7
					}
					nextMonday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 9, 0, 0, 0, loc)
					time.Sleep(time.Until(nextMonday))
					sendWeeklyReports()
				}
			}()

			// --- Oylik hisobot: har oyning 1-kuni 09:00 da ---
			go func() {
				for {
					now := time.Now().In(loc)
					nextMonth := time.Date(now.Year(), now.Month()+1, 1, 9, 0, 0, 0, loc)
					time.Sleep(time.Until(nextMonth))
					sendMonthlyReports()
				}
			}()
		}
	}()

	log.Println("✅ Scheduler ishga tushdi")
}

func main() {
	// Timezone
	loc, err := time.LoadLocation(os.Getenv("TZ"))
	if err != nil || loc == nil {
		loc, _ = time.LoadLocation("Asia/Tashkent")
	}
	time.Local = loc

	InitDB()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN is empty")
	}

	Bot, err = tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Bot started as:", Bot.Self.UserName)

	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Xarajat kiritish"},
		{Command: "daily", Description: "Bugungi statistika"},
		{Command: "weekly", Description: "Shu hafta"},
		{Command: "monthly", Description: "Shu oy"},
	}
	Bot.Request(tgbotapi.NewSetMyCommands(commands...))

	// Schedulerni ishga tushir
	startScheduler(loc)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := Bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID
		text := update.Message.Text

		switch text {
		case "/start":
			Bot.Send(tgbotapi.NewMessage(chatID,
				"👋 Salom! Xarajat botiga xush kelibsiz!\n\nXarajat kiriting:\nMasalan: *15000 ovqat*\n\n📊 Statistika:\n/daily - Bugungi\n/weekly - Haftalik\n/monthly - Oylik"))

		case "/daily":
			sendStats(Bot, chatID, "day")

		case "/weekly":
			sendStats(Bot, chatID, "week")

		case "/monthly":
			sendStats(Bot, chatID, "month")

		default:
			ok, resp := saveExpense(chatID, text)
			Bot.Send(tgbotapi.NewMessage(chatID, resp))

			if ok {
				// Xarajat saqlangandan keyin budget limitni tekshir
				checkBudgetLimit(chatID)
			}
		}
	}
}

func saveExpense(userID int64, text string) (bool, string) {
	parts := strings.SplitN(text, " ", 2)

	if len(parts) < 2 {
		return false, "❌ Format noto'g'ri!\nMasalan: *15000 ovqat*"
	}

	amount, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return false, "❌ Summa noto'g'ri! Faqat raqam kiriting"
	}

	exp := models.Expense{
		UserID:      userID,
		Amount:      amount,
		Description: strings.TrimSpace(parts[1]),
	}

	if err := DB.Create(&exp).Error; err != nil {
		return false, "❌ DB error"
	}

	return true, fmt.Sprintf("✅ Saqlandi: %.0f so'm - %s", amount, exp.Description)
}

func sendStats(bot *tgbotapi.BotAPI, userID int64, period string) {
	var expenses []models.Expense
	var periodName string

	query := DB.Where("user_id = ?", userID)

	switch period {
	case "day":
		query = query.Where("created_at >= NOW() - interval '1 day'")
		periodName = "📆 Bugungi hisobot"
	case "week":
		query = query.Where("created_at >= NOW() - interval '7 days'")
		periodName = "📅 Haftalik hisobot"
	case "month":
		query = query.Where("created_at >= date_trunc('month', NOW())")
		periodName = "🗓 Oylik hisobot"
	}

	err := query.Order("created_at desc").Find(&expenses).Error
	if err != nil {
		log.Println(err)
		return
	}

	if len(expenses) == 0 {
		bot.Send(tgbotapi.NewMessage(userID, "📭 Hech qanday xarajat topilmadi"))
		return
	}

	var total float64
	var result strings.Builder
	result.WriteString(fmt.Sprintf("*%s:*\n\n", periodName))

	for _, e := range expenses {
		total += e.Amount
		result.WriteString(fmt.Sprintf("• %.0f - %s\n", e.Amount, e.Description))
	}

	result.WriteString("\n─────────────────\n")
	result.WriteString(fmt.Sprintf("💰 *Jami: %.0f so'm*", total))

	msg := tgbotapi.NewMessage(userID, result.String())
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}
