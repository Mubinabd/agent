package main

import (
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
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

// ─── Budget limitlar ───────────────────────────────────────────────
const (
	WeeklyLimit  = 1_000_000.0  // 1 million so'm
	MonthlyLimit = 10_000_000.0 // 10 million so'm
)

// ─── DB ───────────────────────────────────────────────────────────
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

	if err = DB.AutoMigrate(&models.Expense{}); err != nil {
		log.Fatal("migration error:", err)
	}

	log.Println("✅ DB muvaffaqiyatli ulandi")
}

// ─── Yordamchi: barcha user IDlar ─────────────────────────────────
func getAllUserIDs() []int64 {
	var userIDs []int64
	DB.Model(&models.Expense{}).Distinct("user_id").Pluck("user_id", &userIDs)
	return userIDs
}

// ─── Budget tekshiruvi: haftalik va oylik ─────────────────────────
func checkBudgetLimit(userID int64) {
	// Haftalik limit
	var weeklyTotal float64
	DB.Model(&models.Expense{}).
		Where("user_id = ? AND created_at >= NOW() - interval '7 days'", userID).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&weeklyTotal)

	if weeklyTotal > WeeklyLimit {
		msg := fmt.Sprintf(
			"⚠️ *Haftalik limit oshdi!*\n\n"+
				"📊 Bu hafta sarflangan: *%.0f so'm*\n"+
				"🔴 Limit: *1,000,000 so'm*\n\n"+
				"Xarajatlarni nazorat qiling! 💡",
			weeklyTotal,
		)
		sendMarkdown(userID, msg)
	}

	// Oylik limit
	var monthlyTotal float64
	DB.Model(&models.Expense{}).
		Where("user_id = ? AND created_at >= date_trunc('month', NOW())", userID).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&monthlyTotal)

	if monthlyTotal > MonthlyLimit {
		msg := fmt.Sprintf(
			"🚨 *Oylik limit oshdi!*\n\n"+
				"📊 Bu oy sarflangan: *%.0f so'm*\n"+
				"🔴 Limit: *10,000,000 so'm*\n\n"+
				"Jiddiy nazorat talab etiladi! 🛑",
			monthlyTotal,
		)
		sendMarkdown(userID, msg)
	}
}

// ─── Kunlik eslatma ────────────────────────────────────────────────
func checkInactiveUsers() {
	log.Println("⏰ Scheduler: Faol bo'lmagan foydalanuvchilar tekshirilmoqda...")
	for _, userID := range getAllUserIDs() {
		var count int64
		DB.Model(&models.Expense{}).
			Where("user_id = ? AND created_at >= NOW() - interval '1 day'", userID).
			Count(&count)

		if count == 0 {
			sendText(userID, "⚠️ Bugun hali xarajat kiritmagansiz!\nUnutmang, xarajatlaringizni kuzatib boring 📊")
			log.Printf("📨 Eslatma yuborildi: userID=%d", userID)
		}
	}
}

// ─── Haftalik hisobot ──────────────────────────────────────────────
func sendWeeklyReports() {
	log.Println("📅 Scheduler: Haftalik hisobotlar yuborilmoqda...")
	for _, userID := range getAllUserIDs() {
		var expenses []models.Expense
		DB.Where("user_id = ? AND created_at >= NOW() - interval '7 days'", userID).
			Order("created_at desc").
			Find(&expenses)

		if len(expenses) == 0 {
			sendText(userID, "📅 Haftalik hisobot: Bu hafta xarajat kiritilmagan.")
			continue
		}

		var total float64
		var sb strings.Builder
		sb.WriteString("📅 *Haftalik hisobot:*\n\n")
		for _, e := range expenses {
			total += e.Amount
			sb.WriteString(fmt.Sprintf("• %.0f - %s\n", e.Amount, e.Description))
		}
		sb.WriteString(fmt.Sprintf("\n─────────────────\n💰 *Jami: %.0f so'm*", total))

		// Limit ogohlantirishi
		if total > WeeklyLimit {
			sb.WriteString(fmt.Sprintf("\n⚠️ *Haftalik limit (1,000,000) %.0f so'mga oshdi!*", total-WeeklyLimit))
		}

		sendMarkdown(userID, sb.String())
		log.Printf("📨 Haftalik hisobot yuborildi: userID=%d", userID)
	}
}

// ─── Oylik hisobot ─────────────────────────────────────────────────
func sendMonthlyReports() {
	log.Println("📅 Scheduler: Oylik hisobotlar yuborilmoqda...")
	for _, userID := range getAllUserIDs() {
		var expenses []models.Expense
		DB.Where(
			"user_id = ? AND created_at >= date_trunc('month', NOW() - interval '1 month') AND created_at < date_trunc('month', NOW())",
			userID,
		).Order("created_at desc").Find(&expenses)

		if len(expenses) == 0 {
			sendText(userID, "🗓 Oylik hisobot: O'tgan oy xarajat kiritilmagan.")
			continue
		}

		categoryMap := make(map[string]float64)
		var total float64
		for _, e := range expenses {
			categoryMap[e.Description] += e.Amount
			total += e.Amount
		}

		var sb strings.Builder
		sb.WriteString("🗓 *Oylik hisobot:*\n\n")
		for desc, sum := range categoryMap {
			percent := (sum / total) * 100
			sb.WriteString(fmt.Sprintf("• %s: %.0f so'm (%.1f%%)\n", desc, sum, percent))
		}
		sb.WriteString(fmt.Sprintf("\n─────────────────\n💰 *Jami: %.0f so'm*\n", total))
		sb.WriteString(fmt.Sprintf("📊 *Jami xarajatlar soni: %d*", len(expenses)))

		// Limit ogohlantirishi
		if total > MonthlyLimit {
			sb.WriteString(fmt.Sprintf("\n🚨 *Oylik limit (10,000,000) %.0f so'mga oshdi!*", total-MonthlyLimit))
		}

		sendMarkdown(userID, sb.String())
		log.Printf("📨 Oylik hisobot yuborildi: userID=%d", userID)
	}
}

// ─── Scheduler ─────────────────────────────────────────────────────
func startScheduler(loc *time.Location) {
	// Kunlik eslatma: har kuni 21:00
	go func() {
		for {
			now := time.Now().In(loc)
			next := time.Date(now.Year(), now.Month(), now.Day(), 21, 0, 0, 0, loc)
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))
			checkInactiveUsers()
		}
	}()

	// Haftalik hisobot: har dushanba 09:00
	go func() {
		for {
			now := time.Now().In(loc)
			days := (int(time.Monday) - int(now.Weekday()) + 7) % 7
			if days == 0 && now.Hour() >= 9 {
				days = 7
			}
			next := time.Date(now.Year(), now.Month(), now.Day()+days, 9, 0, 0, 0, loc)
			time.Sleep(time.Until(next))
			sendWeeklyReports()
		}
	}()

	// Oylik hisobot: har oyning 1-kuni 09:00
	go func() {
		for {
			now := time.Now().In(loc)
			next := time.Date(now.Year(), now.Month()+1, 1, 9, 0, 0, 0, loc)
			time.Sleep(time.Until(next))
			sendMonthlyReports()
		}
	}()

	log.Println("✅ Scheduler ishga tushdi")
}

// ─── Voice → Text (OpenAI Whisper) ────────────────────────────────
func transcribeVoice(fileID string) (string, error) {
	// Telegram'dan fayl URL ni olish
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := Bot.GetFile(fileConfig)
	if err != nil {
		return "", fmt.Errorf("fayl olishda xato: %w", err)
	}

	fileURL := file.Link(Bot.Token)

	// Faylni yuklab olish
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("fayl yuklashda xato: %w", err)
	}
	defer resp.Body.Close()

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fayl o'qishda xato: %w", err)
	}

	// OpenAI Whisper API ga yuborish
	return whisperTranscribe(audioData)
}

// whisperTranscribe - OpenAI Whisper orqali audio → text
func whisperTranscribe(audioData []byte) (string, error) {
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY topilmadi")
	}

	// multipart form data tayyorlash
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer mw.Close()

		fw, _ := mw.CreateFormFile("file", "voice.ogg")
		fw.Write(audioData)
		mw.WriteField("model", "whisper-1")
		mw.WriteField("language", "uz") // O'zbek tili (yoki "ru")
	}()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+openaiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	// JSON parse: {"text": "..."}
	text := extractJSONText(string(body))
	if text == "" {
		return "", fmt.Errorf("transkriptsiya bo'sh qaytdi: %s", string(body))
	}

	return text, nil
}

// Oddiy JSON text extractor ("text":"..." field)
func extractJSONText(jsonStr string) string {
	key := `"text":"`
	idx := strings.Index(jsonStr, key)
	if idx == -1 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(jsonStr[start:], `"`)
	if end == -1 {
		return ""
	}
	return jsonStr[start : start+end]
}

// ─── Xarajat saqlash ───────────────────────────────────────────────
func saveExpense(userID int64, text string) (bool, string) {
	parts := strings.SplitN(strings.TrimSpace(text), " ", 2)

	if len(parts) < 2 {
		return false, "❌ Format noto'g'ri!\nMasalan: *15000 ovqat*"
	}

	amount, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || amount <= 0 {
		return false, "❌ Summa noto'g'ri! Faqat musbat raqam kiriting"
	}

	exp := models.Expense{
		UserID:      userID,
		Amount:      amount,
		Description: strings.TrimSpace(parts[1]),
	}

	if err := DB.Create(&exp).Error; err != nil {
		log.Printf("DB xato (userID=%d): %v", userID, err)
		return false, "❌ Saqlashda xato yuz berdi"
	}

	return true, fmt.Sprintf("✅ Saqlandi: *%.0f so'm* - %s", amount, exp.Description)
}

// ─── Statistika yuborish ───────────────────────────────────────────
func sendStats(userID int64, period string) {
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

	if err := query.Order("created_at desc").Find(&expenses).Error; err != nil {
		log.Printf("sendStats xato: %v", err)
		return
	}

	if len(expenses) == 0 {
		sendText(userID, "📭 Hech qanday xarajat topilmadi")
		return
	}

	var total float64
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%s:*\n\n", periodName))

	for _, e := range expenses {
		total += e.Amount
		sb.WriteString(fmt.Sprintf("• %.0f - %s\n", e.Amount, e.Description))
	}

	sb.WriteString(fmt.Sprintf("\n─────────────────\n💰 *Jami: %.0f so'm*", total))

	// Limit ko'rsatish
	switch period {
	case "week":
		remaining := WeeklyLimit - total
		if remaining > 0 {
			sb.WriteString(fmt.Sprintf("\n✅ Haftalik limitdan qolgan: *%.0f so'm*", remaining))
		} else {
			sb.WriteString(fmt.Sprintf("\n⚠️ Haftalik limit *%.0f so'mga* oshgan!", -remaining))
		}
	case "month":
		remaining := MonthlyLimit - total
		if remaining > 0 {
			sb.WriteString(fmt.Sprintf("\n✅ Oylik limitdan qolgan: *%.0f so'm*", remaining))
		} else {
			sb.WriteString(fmt.Sprintf("\n🚨 Oylik limit *%.0f so'mga* oshgan!", -remaining))
		}
	}

	sendMarkdown(userID, sb.String())
}

// ─── Yuborish yordamchilari ────────────────────────────────────────
func sendText(userID int64, text string) {
	Bot.Send(tgbotapi.NewMessage(userID, text))
}

func sendMarkdown(userID int64, text string) {
	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "Markdown"
	Bot.Send(msg)
}

// ─── Main ──────────────────────────────────────────────────────────
func main() {
	loc, err := time.LoadLocation(os.Getenv("TZ"))
	if err != nil || loc == nil {
		loc, _ = time.LoadLocation("Asia/Tashkent")
	}
	time.Local = loc

	InitDB()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN topilmadi")
	}

	Bot, err = tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("🤖 Bot ishga tushdi:", Bot.Self.UserName)

	Bot.Request(tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Xarajat kiritish"},
		tgbotapi.BotCommand{Command: "daily", Description: "Bugungi statistika"},
		tgbotapi.BotCommand{Command: "weekly", Description: "Haftalik statistika"},
		tgbotapi.BotCommand{Command: "monthly", Description: "Oylik statistika"},
	))

	startScheduler(loc)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := Bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID

		// ── Voice xabar ──────────────────────────────────────────
		if update.Message.Voice != nil {
			sendText(chatID, "🎤 Ovozli xabar qabul qilindi, matngа aylantirilmoqda...")

			transcribed, err := transcribeVoice(update.Message.Voice.FileID)
			if err != nil {
				log.Printf("Voice transcribe xato (userID=%d): %v", chatID, err)
				sendText(chatID, "❌ Ovozli xabarni o'qishda xato. Iltimos, matn kiriting.")
				continue
			}

			log.Printf("🎤 Transkriptsiya (userID=%d): %s", chatID, transcribed)
			sendText(chatID, fmt.Sprintf("🗒 Eshitildi: _%s_", transcribed))

			// Xarajat sifatida saqlashga urinish
			ok, resp := saveExpense(chatID, transcribed)
			sendMarkdown(chatID, resp)
			if ok {
				checkBudgetLimit(chatID)
			}
			continue
		}

		// ── Matn xabar ───────────────────────────────────────────
		text := update.Message.Text
		switch text {
		case "/start":
			sendMarkdown(chatID,
				"👋 *Xarajat botiga xush kelibsiz!*\n\n"+
					"💬 Xarajat kiriting:\n`15000 ovqat`\n\n"+
					"🎤 Yoki ovozli xabar yuboring!\n\n"+
					"📊 *Statistika:*\n"+
					"/daily — Bugungi\n"+
					"/weekly — Haftalik (limit: 1,000,000 so'm)\n"+
					"/monthly — Oylik (limit: 10,000,000 so'm)",
			)

		case "/daily":
			sendStats(chatID, "day")

		case "/weekly":
			sendStats(chatID, "week")

		case "/monthly":
			sendStats(chatID, "month")

		default:
			ok, resp := saveExpense(chatID, text)
			sendMarkdown(chatID, resp)
			if ok {
				checkBudgetLimit(chatID)
			}
		}
	}
}
