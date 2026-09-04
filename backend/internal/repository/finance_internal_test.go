package repository

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newInternalFinanceRepository(t *testing.T) (*Repository, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "finance.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_txlock=immediate&_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	if err := db.AutoMigrate(&model.User{}, &model.CreditAccount{}, &model.BillingOrder{}, &model.CreditLedgerEntry{}); err != nil {
		t.Fatal(err)
	}
	return &Repository{db: db}, db
}

func newInternalBillingOrder(userID, key string, amount int64) *model.BillingOrder {
	return &model.BillingOrder{
		ID:                         newRepositoryID(),
		UserID:                     userID,
		IdempotencyKey:             key,
		Model:                      "canvas_get_context",
		Capability:                 "mcp",
		Scene:                      "mcp",
		BillingMode:                "fixed_request",
		UnitPriceMicrocredits:      amount,
		MultiplierBasisPoints:      10_000,
		Quantity:                   1,
		AmountMicrocredits:         amount,
		ReservedAmountMicrocredits: amount,
		Status:                     model.BillingStatusReserved,
	}
}

func createInternalUserAndAccount(t *testing.T, db *gorm.DB, userID string, status model.UserStatus, available int64) {
	t.Helper()
	if err := db.Create(&model.User{ID: userID, Username: userID, Role: model.UserRoleUser, Status: status}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CreditAccount{UserID: userID, AvailableMicrocredits: available}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReserveBillingOrderIdempotentRejectsNonPositiveAmount(t *testing.T) {
	repo, _ := newInternalFinanceRepository(t)
	for _, tt := range []struct {
		name   string
		amount int64
	}{
		{name: "zero", amount: 0},
		{name: "negative", amount: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			order := newInternalBillingOrder("user-1", "key-"+tt.name, tt.amount)
			if _, err := repo.ReserveBillingOrderIdempotent(order); !errors.Is(err, ErrInvalidBillingAmount) {
				t.Fatalf("ReserveBillingOrderIdempotent(%d) error = %v, want %v", tt.amount, err, ErrInvalidBillingAmount)
			}
		})
	}
}

func TestReserveBillingOrderIdempotentIsIdempotentAndDetectsConflict(t *testing.T) {
	repo, db := newInternalFinanceRepository(t)
	createInternalUserAndAccount(t, db, "user-1", model.UserStatusActive, 100)

	first, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", "request-1", 60))
	if err != nil {
		t.Fatalf("first reserve error = %v", err)
	}
	second, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", "request-1", 60))
	if err != nil {
		t.Fatalf("idempotent reserve error = %v", err)
	}
	if second.ID != first.ID || second.Status != model.BillingStatusReserved {
		t.Fatalf("idempotent reserve = %#v, want order %s reserved", second, first.ID)
	}

	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user-1").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicrocredits != 40 || account.ReservedMicrocredits != 60 {
		t.Fatalf("account after idempotent reserve = %#v", account)
	}
	var orderCount, reserveCount int64
	if err := db.Model(&model.BillingOrder{}).Where("user_id = ? AND idempotency_key = ?", "user-1", "request-1").Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditLedgerEntry{}).Where("billing_order_id = ? AND type = ?", first.ID, model.CreditLedgerReserve).Count(&reserveCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 || reserveCount != 1 {
		t.Fatalf("idempotent rows = order %d reserve ledger %d", orderCount, reserveCount)
	}

	conflict := newInternalBillingOrder("user-1", "request-1", 50)
	if _, err := repo.ReserveBillingOrderIdempotent(conflict); !errors.Is(err, ErrBillingIdempotencyConflict) {
		t.Fatalf("conflicting reserve error = %v, want %v", err, ErrBillingIdempotencyConflict)
	}
}

func TestReserveBillingOrderIdempotentRollsBackInsufficientCredits(t *testing.T) {
	repo, db := newInternalFinanceRepository(t)
	createInternalUserAndAccount(t, db, "user-1", model.UserStatusActive, 10)

	if _, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", "request-1", 20)); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("reserve error = %v, want %v", err, ErrInsufficientCredits)
	}
	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user-1").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicrocredits != 10 || account.ReservedMicrocredits != 0 {
		t.Fatalf("account after failed reserve = %#v", account)
	}
	var orderCount, ledgerCount int64
	if err := db.Model(&model.BillingOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditLedgerEntry{}).Count(&ledgerCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 0 || ledgerCount != 0 {
		t.Fatalf("failed reserve left rows: orders=%d ledger=%d", orderCount, ledgerCount)
	}
}

func TestReserveBillingOrderIdempotentConcurrentReservationsDoNotOverdraw(t *testing.T) {
	repo, db := newInternalFinanceRepository(t)
	createInternalUserAndAccount(t, db, "user-1", model.UserStatusActive, 100)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"request-1", "request-2"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			_, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", key, 60))
			results <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)

	var success int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if !errors.Is(err, ErrInsufficientCredits) {
			t.Fatalf("concurrent reserve error = %v, want nil or %v", err, ErrInsufficientCredits)
		}
	}
	if success != 1 {
		t.Fatalf("successful concurrent reservations = %d, want 1", success)
	}
	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user-1").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicrocredits != 40 || account.ReservedMicrocredits != 60 {
		t.Fatalf("account after concurrent reserve = %#v", account)
	}
}

func TestBillingOrderTransitionsAreIdempotent(t *testing.T) {
	repo, db := newInternalFinanceRepository(t)
	createInternalUserAndAccount(t, db, "user-1", model.UserStatusActive, 100)

	settleOrder, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", "settle-key", 30))
	if err != nil {
		t.Fatalf("settle reserve error = %v", err)
	}
	if err := repo.SettleBillingOrder(settleOrder.ID, "settle-key"); err != nil {
		t.Fatalf("settle error = %v", err)
	}
	if err := repo.SettleBillingOrder(settleOrder.ID, "settle-key"); err != nil {
		t.Fatalf("idempotent settle error = %v", err)
	}

	refundOrder, err := repo.ReserveBillingOrderIdempotent(newInternalBillingOrder("user-1", "refund-key", 20))
	if err != nil {
		t.Fatalf("refund reserve error = %v", err)
	}
	if err := repo.RefundBillingOrder(refundOrder.ID, "tool failed"); err != nil {
		t.Fatalf("refund error = %v", err)
	}
	if err := repo.RefundBillingOrder(refundOrder.ID, "tool failed again"); err != nil {
		t.Fatalf("idempotent refund error = %v", err)
	}

	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user-1").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicrocredits != 70 || account.ReservedMicrocredits != 0 {
		t.Fatalf("account after transitions = %#v", account)
	}
	var consumeCount, refundCount int64
	if err := db.Model(&model.CreditLedgerEntry{}).Where("billing_order_id = ? AND type = ?", settleOrder.ID, model.CreditLedgerConsume).Count(&consumeCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditLedgerEntry{}).Where("billing_order_id = ? AND type = ?", refundOrder.ID, model.CreditLedgerRefund).Count(&refundCount).Error; err != nil {
		t.Fatal(err)
	}
	if consumeCount != 1 || refundCount != 1 {
		t.Fatalf("transition ledger counts = consume %d refund %d", consumeCount, refundCount)
	}
}
