package hub

import (
	"testing"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

func policyWith(accountRate float64, accountBurst int, botRate float64, botBurst, concurrency int) config.Policy {
	policy := config.Default().Policy
	policy.PerAccountRate = config.RateLimit{Rate: accountRate, Burst: accountBurst}
	policy.PerBotRate = config.RateLimit{Rate: botRate, Burst: botBurst, Concurrency: concurrency}
	return policy
}

func TestBucketBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBucket(1, 3, now)

	for i := 0; i < 3; i++ {
		if ok, _ := b.take(now); !ok {
			t.Fatalf("token %d should be available in the burst", i+1)
		}
	}
	ok, wait := b.take(now)
	if ok {
		t.Fatal("the burst must be exhausted")
	}
	if wait <= 0 || wait > time.Second {
		t.Fatalf("wait = %v, want about 1s", wait)
	}

	// Half a second later only half a token has accumulated.
	if ok, _ := b.take(now.Add(500 * time.Millisecond)); ok {
		t.Fatal("half a token must not allow an action")
	}
	// A full second restores exactly one token.
	if ok, _ := b.take(now.Add(time.Second)); !ok {
		t.Fatal("one token should have refilled after 1s")
	}
	// Long idle time does not exceed the burst.
	if ok, _ := b.take(now.Add(time.Hour)); !ok {
		t.Fatal("a long idle period should refill up to the burst")
	}
	if ok, _ := b.take(now.Add(time.Hour)); !ok {
		t.Fatal("second token of the refilled burst")
	}
	if ok, _ := b.take(now.Add(time.Hour)); !ok {
		t.Fatal("third token of the refilled burst")
	}
	if ok, _ := b.take(now.Add(time.Hour)); ok {
		t.Fatal("the refill must stop at the burst size")
	}
}

func TestAccountBucketIsSharedAcrossBots(t *testing.T) {
	limiter := NewLimiter(policyWith(1, 2, 0, 0, 0))
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }

	// Two Bots, one account: together they get the burst, not the burst each.
	if ok, _ := limiter.AllowAccount("10001"); !ok {
		t.Fatal("first action should pass")
	}
	if ok, _ := limiter.AllowAccount("10001"); !ok {
		t.Fatal("second action should pass")
	}
	if ok, wait := limiter.AllowAccount("10001"); ok || wait <= 0 {
		t.Fatalf("third action should be limited, got ok=%v wait=%v", ok, wait)
	}

	// A different account has its own bucket.
	if ok, _ := limiter.AllowAccount("10002"); !ok {
		t.Fatal("another account must not be affected")
	}

	// After the refill window the account works again.
	now = now.Add(time.Second)
	if ok, _ := limiter.AllowAccount("10001"); !ok {
		t.Fatal("bucket did not refill")
	}

	account, bot, inflight := limiter.Counters()
	if account != 1 || bot != 0 || inflight != 0 {
		t.Fatalf("counters = %d/%d/%d, want 1/0/0", account, bot, inflight)
	}
}

func TestPerBotQuotaIsIndependent(t *testing.T) {
	limiter := NewLimiter(policyWith(0, 0, 1, 1, 0))
	now := time.Unix(200, 0)
	limiter.now = func() time.Time { return now }

	if ok, _ := limiter.AllowBot("bot-a", "10001"); !ok {
		t.Fatal("bot-a first action should pass")
	}
	if ok, _ := limiter.AllowBot("bot-a", "10001"); ok {
		t.Fatal("bot-a quota should be exhausted")
	}
	// Same Bot, another account: separate bucket.
	if ok, _ := limiter.AllowBot("bot-a", "10002"); !ok {
		t.Fatal("bot-a should still be able to use another account")
	}
	// Another Bot is unaffected.
	if ok, _ := limiter.AllowBot("bot-b", "10001"); !ok {
		t.Fatal("bot-b must not be throttled by bot-a")
	}

	now = now.Add(2 * time.Second)
	if ok, _ := limiter.AllowBot("bot-a", "10001"); !ok {
		t.Fatal("bot quota did not refill")
	}
	if _, bot, _ := limiter.Counters(); bot == 0 {
		t.Fatal("bot-side limit was not counted")
	}
}

func TestConcurrencyCap(t *testing.T) {
	limiter := NewLimiter(policyWith(0, 0, 0, 0, 2))
	if !limiter.Acquire("bot-a") || !limiter.Acquire("bot-a") {
		t.Fatal("two slots should be available")
	}
	if limiter.Acquire("bot-a") {
		t.Fatal("third slot must be refused")
	}
	if !limiter.Acquire("bot-b") {
		t.Fatal("another Bot has its own slots")
	}
	if limiter.Inflight("bot-a") != 2 {
		t.Fatalf("inflight = %d, want 2", limiter.Inflight("bot-a"))
	}
	limiter.Release("bot-a")
	if !limiter.Acquire("bot-a") {
		t.Fatal("released slot should be reusable")
	}
	// Over-release must not corrupt the counter.
	limiter.Release("bot-a")
	limiter.Release("bot-a")
	limiter.Release("bot-a")
	if limiter.Inflight("bot-a") != 0 {
		t.Fatalf("inflight after over-release = %d, want 0", limiter.Inflight("bot-a"))
	}
	if _, _, refused := limiter.Counters(); refused != 1 {
		t.Fatalf("inflight refusals = %d, want 1", refused)
	}
}

func TestLimiterDisabledWhenRateIsZero(t *testing.T) {
	limiter := NewLimiter(policyWith(0, 0, 0, 0, 0))
	for i := 0; i < 100; i++ {
		if ok, _ := limiter.AllowAccount("1"); !ok {
			t.Fatal("rate 0 disables account limiting")
		}
		if ok, _ := limiter.AllowBot("b", "1"); !ok {
			t.Fatal("rate 0 disables bot limiting")
		}
		if !limiter.Acquire("b") {
			t.Fatal("concurrency 0 disables the in-flight cap")
		}
	}
	if limiter.Buckets() != 0 {
		t.Fatalf("no buckets should be created when limits are off, got %d", limiter.Buckets())
	}
}

func TestReadOnlyActions(t *testing.T) {
	readOnly := []string{"get_status", "get_msg", "can_send_image", "get_group_info", ".get_version_info", "get_login_info", "can_send_record", "hub_list_accounts"}
	for _, action := range readOnly {
		if !ReadOnlyAction(action) {
			t.Fatalf("%s should be read-only", action)
		}
	}
	active := []string{"send_msg", "send_group_msg", "set_group_ban", "delete_friend", "set_group_card", ""}
	for _, action := range active {
		if ReadOnlyAction(action) {
			t.Fatalf("%s should count as an active action", action)
		}
	}
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	limiter := NewLimiter(policyWith(1, 1, 1, 1, 0))
	now := time.Unix(0, 0)
	limiter.now = func() time.Time { return now }

	limiter.AllowAccount("10001")
	limiter.AllowBot("bot-a", "10001")
	if limiter.Buckets() != 2 {
		t.Fatalf("buckets = %d, want 2", limiter.Buckets())
	}
	if removed := limiter.Sweep(time.Minute); removed != 0 {
		t.Fatalf("nothing should be swept yet, removed %d", removed)
	}
	now = now.Add(2 * time.Hour)
	if removed := limiter.Sweep(time.Minute); removed != 2 {
		t.Fatalf("idle buckets removed = %d, want 2", removed)
	}
	if limiter.Buckets() != 0 {
		t.Fatalf("buckets = %d, want 0", limiter.Buckets())
	}
}
