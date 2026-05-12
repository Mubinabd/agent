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
	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"agent/models"
)

// ─── Config ───────────────────────────────────────────────────────
type Config struct {
	BotToken    string `yaml:"bot_token"`
	DatabaseURL string `yaml:"database_url"`
	OpenAIKey   string `yaml:"openai_api_key"`
	Timezone    string `yaml:"timezone"`
	Budget      struct {
		WeeklyLimit  float64 `yaml:"weekly_limit"`
		MonthlyLimit float64 `yaml:"monthly_limit"`
	} `yaml:"budget"`
}

var Cfg Config

func loadConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ config.yaml o'qishda xato: %v", err)
	}
	if err := yaml.Unmarshal(data, &Cfg); err != nil {
		log.Fatalf("❌ config.yaml parse xato: %v", err)
	}
	if Cfg.Budget.WeeklyLimit == 0 {
		Cfg.Budget.WeeklyLimit = 1_000_000
	}
	if Cfg.Budget.MonthlyLimit == 0 {
		Cfg.Budget.MonthlyLimit = 10_000_000
	}
	if Cfg.Timezone == "" {
		Cfg.Timezone = "Asia/Tashkent"
	}
	log.Println("✅ Config yuklandi")
}

// ─── Global vars ──────────────────────────────────────────────────
var DB *gorm.DB
var Bot *tgbotapi.BotAPI

// ─── DB ───────────────────────────────────────────────────────────
func InitDB() {
	var db *gorm.DB
	var err error

	for i := 0; i < 10; i++ {
		db, err = gorm.Open(postgres.Open(Cfg.DatabaseURL), &gorm.Config{})
		if err == nil {
			break
		}
		log.Printf("DB ulanishda xato, qayta urinilmoqda... (%d/10)", i+1)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		log.Fatal("❌ DB ga ulanib bo'lmadi:", err)
	}
	DB = db
	if err = DB.AutoMigrate(&models.Expense{}); err != nil {
		log.Fatal("migration error:", err)
	}
	log.Println("✅ DB muvaffaqiyatli ulandi")
}

// ─── Yordamchi ────────────────────────────────────────────────────
func getAllUserIDs() []int64 {
	var userIDs []int64
	DB.Model(&models.Expense{}).Distinct("user_id").Pluck("user_id", &userIDs)
	return userIDs
}

// ─── Budget tekshiruvi ─────────────────────────────────────────────
func checkBudgetLimit(userID int64) {
	var weeklyTotal float64
	DB.Model(&models.Expense{}).
		Where("user_id = ? AND created_at >= NOW() - interval '7 days'", userID).
		Select("COALESCE(SUM(amount), 0)").Scan(&weeklyTotal)

	if weeklyTotal > Cfg.Budget.WeeklyLimit {
		sendMarkdown(userID, fmt.Sprintf(
			"⚠️ *Haftalik limit oshdi!*\n\n📊 Bu hafta: *%.0f so'm*\n🔴 Limit: *%.0f so'm*\n\nXarajatlarni nazorat qiling! 💡",
			weeklyTotal, Cfg.Budget.WeeklyLimit,
		))
	}

	var monthlyTotal float64
	DB.Model(&models.Expense{}).
		Where("user_id = ? AND created_at >= date_trunc('month', NOW())", userID).
		Select("COALESCE(SUM(amount), 0)").Scan(&monthlyTotal)

	if monthlyTotal > Cfg.Budget.MonthlyLimit {
		sendMarkdown(userID, fmt.Sprintf(
			"🚨 *Oylik limit oshdi!*\n\n📊 Bu oy: *%.0f so'm*\n🔴 Limit: *%.0f so'm*\n\nJiddiy nazorat talab etiladi! 🛑",
			monthlyTotal, Cfg.Budget.MonthlyLimit,
		))
	}
}

// ─── Kunlik eslatma ────────────────────────────────────────────────
func checkInactiveUsers() {
	log.Println("⏰ Scheduler: Faol bo'lmagan foydalanuvchilar tekshirilmoqda...")
	for _, userID := range getAllUserIDs() {
		var count int64
		DB.Model(&models.Expense{}).
			Where("user_id = ? AND created_at >= NOW() - interval '1 day'", userID).Count(&count)
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
			Order("created_at desc").Find(&expenses)

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
		if total > Cfg.Budget.WeeklyLimit {
			sb.WriteString(fmt.Sprintf("\n⚠️ *Limit %.0f so'mga oshdi!*", total-Cfg.Budget.WeeklyLimit))
		}
		sendMarkdown(userID, sb.String())
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
			sb.WriteString(fmt.Sprintf("• %s: %.0f so'm (%.1f%%)\n", desc, sum, (sum/total)*100))
		}
		sb.WriteString(fmt.Sprintf("\n─────────────────\n💰 *Jami: %.0f so'm*\n", total))
		sb.WriteString(fmt.Sprintf("📊 *Jami xarajatlar soni: %d*", len(expenses)))
		if total > Cfg.Budget.MonthlyLimit {
			sb.WriteString(fmt.Sprintf("\n🚨 *Limit %.0f so'mga oshdi!*", total-Cfg.Budget.MonthlyLimit))
		}
		sendMarkdown(userID, sb.String())
	}
}

// ─── Scheduler ─────────────────────────────────────────────────────
func startScheduler(loc *time.Location) {
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

// ─── Voice → Text ──────────────────────────────────────────────────
func transcribeVoice(fileID string) (string, error) {
	file, err := Bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("fayl olishda xato: %w", err)
	}
	resp, err := http.Get(file.Link(Bot.Token))
	if err != nil {
		return "", fmt.Errorf("fayl yuklashda xato: %w", err)
	}
	defer resp.Body.Close()
	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fayl o'qishda xato: %w", err)
	}
	return whisperTranscribe(audioData)
}

func whisperTranscribe(audioData []byte) (string, error) {
	if Cfg.OpenAIKey == "" {
		return "", fmt.Errorf("openai_api_key config.yaml da topilmadi")
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.Close()
		fw, _ := mw.CreateFormFile("file", "voice.ogg")
		fw.Write(audioData)
		mw.WriteField("model", "whisper-1")
	}()
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+Cfg.OpenAIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	text := extractJSONField(string(body), "text")
	if text == "" {
		return "", fmt.Errorf("transkriptsiya bo'sh: %s", string(body))
	}
	log.Printf("🎤 Whisper raw: %q", text)
	return text, nil
}

// ─── GPT: erkin matn → "summa tavsif" ─────────────────────────────
func parseExpenseFromText(rawText string) (string, error) {
	if Cfg.OpenAIKey == "" {
		return "", fmt.Errorf("openai_api_key topilmadi")
	}
	prompt := fmt.Sprintf(`Quyidagi ovozli xabardan xarajat ma'lumotini ajrat.

Matn: "%s"

Faqat quyidagi formatda javob ber (boshqa hech narsa yozma):
SUMMA TAVSIF

Qoidalar:
- SUMMA: faqat raqam (so'zlarni raqamga o'zgartir, masalan "o'n besh ming" = 15000)
- TAVSIF: xarajat nomi (1-2 so'z, o'zbek tilida)
- Agar xarajat ma'lumoti topilmasa, faqat "ERROR" yaz

Misollar:
"o'n besh ming so'm ovqatga" → 15000 ovqat
"taksi uchun yigirma ming" → 20000 taksi
"ming besh yuz supermarketga" → 1500 supermarket`, rawText)

	jsonBody := fmt.Sprintf(`{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":%q}]}`, prompt)
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", strings.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+Cfg.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	content := strings.TrimSpace(extractJSONField(string(body), "content"))
	log.Printf("🤖 GPT parse: %q → %q", rawText, content)
	if content == "" || content == "ERROR" {
		return "", fmt.Errorf("xarajat ma'lumoti topilmadi")
	}
	return content, nil
}

// ─── JSON field extractor ──────────────────────────────────────────
func extractJSONField(jsonStr, field string) string {
	key := fmt.Sprintf(`"%s":"`, field)
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
		return false, "❌ Format noto'g'ri!\nMasalan: `15000 ovqat`"
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

// ─── Statistika ────────────────────────────────────────────────────
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
	switch period {
	case "week":
		rem := Cfg.Budget.WeeklyLimit - total
		if rem > 0 {
			sb.WriteString(fmt.Sprintf("\n✅ Limitdan qolgan: *%.0f so'm*", rem))
		} else {
			sb.WriteString(fmt.Sprintf("\n⚠️ Limit *%.0f so'mga* oshgan!", -rem))
		}
	case "month":
		rem := Cfg.Budget.MonthlyLimit - total
		if rem > 0 {
			sb.WriteString(fmt.Sprintf("\n✅ Limitdan qolgan: *%.0f so'm*", rem))
		} else {
			sb.WriteString(fmt.Sprintf("\n🚨 Limit *%.0f so'mga* oshgan!", -rem))
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
	loadConfig("config.yaml")

	loc, err := time.LoadLocation(Cfg.Timezone)
	if err != nil || loc == nil {
		loc, _ = time.LoadLocation("Asia/Tashkent")
	}
	time.Local = loc

	InitDB()

	if Cfg.BotToken == "" {
		log.Fatal("❌ bot_token config.yaml da topilmadi")
	}
	Bot, err = tgbotapi.NewBotAPI(Cfg.BotToken)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("🤖 Bot ishga tushdi:", Bot.Self.UserName)

	Bot.Request(tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Xarajat kiritish"},
		tgbotapi.BotCommand{Command: "daily", Description: "Bugungi statistika"},
		tgbotapi.BotCommand{Command: "weekly", Description: "Shu hafta"},
		tgbotapi.BotCommand{Command: "monthly", Description: "Shu oy"},
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

		if update.Message.Voice != nil {
			sendText(chatID, "🎤 Ovozli xabar qabul qilindi...")
			rawText, err := transcribeVoice(update.Message.Voice.FileID)
			if err != nil {
				log.Printf("❌ Whisper xato (userID=%d): %v", chatID, err)
				sendText(chatID, "❌ Ovozni o'qishda xato. Iltimos, matn kiriting.")
				continue
			}
			parsed, err := parseExpenseFromText(rawText)
			if err != nil {
				log.Printf("❌ GPT parse xato (userID=%d): %v", chatID, err)
				sendMarkdown(chatID, fmt.Sprintf(
					"🎤 Eshitildi: _%s_\n\n❌ Xarajat formatini aniqlab bo'lmadi.\nIltimos qo'lda kiriting:\n`15000 ovqat`",
					rawText,
				))
				continue
			}
			sendMarkdown(chatID, fmt.Sprintf("🎤 Eshitildi: _%s_", rawText))
			ok, resp := saveExpense(chatID, parsed)
			sendMarkdown(chatID, resp)
			if ok {
				checkBudgetLimit(chatID)
			}
			continue
		}

		text := update.Message.Text
		switch text {
		case "/start":
			sendMarkdown(chatID,
				"👋 *Xarajat botiga xush kelibsiz!*\n\n"+
					"💬 Xarajat kiriting:\n`15000 ovqat`\n\n"+
					"🎤 Yoki ovozli xabar yuboring!\n\n"+
					"📊 *Statistika:*\n"+
					"/daily — Bugungi\n"+
					fmt.Sprintf("/weekly — Haftalik (limit: %.0f so'm)\n", Cfg.Budget.WeeklyLimit)+
					fmt.Sprintf("/monthly — Oylik (limit: %.0f so'm)", Cfg.Budget.MonthlyLimit),
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
