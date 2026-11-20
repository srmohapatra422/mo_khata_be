package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // shop timezones work even where the OS has no tz database

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/billing"
	"mohisab-api/internal/catalog"
	"mohisab-api/internal/catalogue"
	"mohisab-api/internal/closeday"
	"mohisab-api/internal/config"
	"mohisab-api/internal/customers"
	"mohisab-api/internal/dashboard"
	"mohisab-api/internal/db"
	"mohisab-api/internal/devauth"
	"mohisab-api/internal/expenses"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/idem"
	"mohisab-api/internal/lookup"
	"mohisab-api/internal/otp"
	"mohisab-api/internal/pricing"
	"mohisab-api/internal/profile"
	"mohisab-api/internal/rewards"
	"mohisab-api/internal/setup"
	"mohisab-api/internal/shops"
	"mohisab-api/internal/stock"
	"mohisab-api/internal/uploads"
	"mohisab-api/internal/voice"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	verifier, err := auth.NewVerifier(ctx, cfg.SupabaseURL, cfg.SupabaseJWTSecret, cfg.DevAuthSecret)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	verifier = verifier.WithRevocation(pool)

	if n, err := catalog.BackfillItems(ctx, pool); err != nil {
		log.Printf("WARNING: catalog backfill: %v", err)
	} else if n > 0 {
		log.Printf("catalog backfill: linked %d old items to categories", n)
	}

	// Logo uploads go through Supabase Storage with the service key (server side only).
	var storage *uploads.Storage
	logoPrefix := ""
	if cfg.SupabaseServiceKey != "" {
		storage = uploads.NewStorage(cfg.StorageURL, cfg.SupabaseServiceKey)
		logoPrefix = storage.PublicPrefix()
		if err := storage.EnsureBucket(ctx); err != nil {
			log.Printf("WARNING: storage bucket %q not ready: %v", uploads.LogoBucket, err)
		}
	} else {
		log.Println("logo upload disabled: set SUPABASE_SERVICE_KEY to enable POST /v1/uploads/logo")
	}
	uploadH := uploads.Handler{Storage: storage}

	shopH := shops.Handler{DB: pool, LogoPrefix: logoPrefix, TrialDays: cfg.TrialDays}
	profH := profile.Handler{DB: pool, LogoPrefix: logoPrefix, TrialDays: cfg.TrialDays, Storage: storage}
	catH := catalogue.Handler{DB: pool}
	custH := customers.Handler{DB: pool, GoldSpend: cfg.GoldSpend, SilverSpend: cfg.SilverSpend}
	billH := billing.Handler{DB: pool}
	priceH := pricing.Handler{DB: pool}
	stockH := stock.Handler{DB: pool}
	dashH := dashboard.Handler{DB: pool}
	setupH := setup.Handler{DB: pool}
	expH := expenses.Handler{DB: pool}
	closeH := closeday.Handler{DB: pool}
	rewardH := rewards.Handler{DB: pool}
	// Rule-based for now. Put an LLM parser here later; it only has to implement voice.Parser.
	voiceH := voice.Handler{DB: pool, Parser: voice.RuleParser{}}
	lookupH := lookup.Handler{DB: pool}
	idemS := &idem.Store{DB: pool}
	// Dev stub: any phone, any non-empty code passes. Swap for a real provider later.
	otpH := otp.Handler{DB: pool, Provider: otp.DevProvider{}, DevMode: true}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// DEV-ONLY phone login (any number, any code). Enabled only when DEV_AUTH_SECRET is set.
	if cfg.DevAuthSecret != "" {
		log.Println("WARNING: dev auth enabled (/auth/otp/*): any phone and any code can log in")
		devH := devauth.Handler{
			DB: pool, Provider: otp.DevProvider{}, Secret: cfg.DevAuthSecret,
			Issuer: strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1",
		}
		r.Post("/auth/otp/send", devH.Send)
		r.Post("/auth/otp/verify", devH.Verify)
	}

	// Logging out revokes the token on the server (it is rejected from then on).
	r.With(verifier.RequireUser).Post("/auth/logout", auth.Logout(pool))

	r.Route("/v1", func(r chi.Router) {
		r.Use(verifier.RequireUser)

		r.Get("/me", shopH.Me)
		r.Patch("/me", profH.PatchMe)
		r.Post("/shops", shopH.Create)
		r.Post("/uploads/logo", uploadH.Logo)

		// Everything below needs X-Shop-ID and membership.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireShop(pool))

			r.Get("/prices/today", catH.Today)

			// Profile screen.
			r.Get("/profile", profH.Get)
			r.Get("/locations", profH.GetLocations)
			r.Get("/offer", profH.GetOffer)
			r.Get("/offer/preview", profH.Preview)
			r.Get("/devices", profH.GetDevices)
			r.With(auth.RequireRoles("owner", "cashier")).Put("/devices/{device}", profH.PutDevice)
			r.With(auth.RequireRoles("owner", "cashier")).Get("/staff", profH.ListStaff)
			r.Get("/customers", custH.List)
			r.Get("/customers/summary", custH.Summary)
			r.Get("/customers/{id}", custH.Get)
			r.Get("/customers/{id}/points", custH.Points)
			r.With(auth.RequireRoles("owner", "cashier")).Patch("/customers/{id}", custH.Patch)
			r.With(auth.RequireRoles("owner", "cashier"), idemS.Middleware).Post("/customers/{id}/payments", custH.Pay)
			r.Get("/bills", billH.List)
			r.Get("/bills/{id}", billH.Get)
			r.With(idemS.Middleware).Post("/bills", billH.Create)
			r.Post("/points/otp/send", otpH.Send)
			r.Post("/points/otp/verify", otpH.Verify)

			r.Get("/dashboard", dashH.Get)
			r.Post("/voice/parse", voiceH.Parse)
			r.Get("/rewards", rewardH.Get)
			r.Get("/setup", setupH.Get)

			// Expenses (farm managers: farm location only) and the end-of-day count.
			r.Get("/expenses", expH.List)
			r.Post("/expenses", expH.Create)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRoles("owner", "cashier"))
				r.Get("/close-day/preview", closeH.Preview)
				r.With(idemS.Middleware).Post("/close-day", closeH.Close)
			})

			// Categories and prices.
			r.Get("/categories", priceH.Categories)
			r.Get("/prices", priceH.List)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRoles("owner", "cashier"))
				r.Post("/prices/items", priceH.CreateItem)
				r.Put("/prices", priceH.SaveRates)
				r.Post("/prices/keep-yesterday", priceH.KeepYesterday)
				r.Patch("/prices/items/{id}", priceH.PatchItem)
			})

			// Stock. Farm managers only get the farm location (see internal/access).
			r.Get("/stock", stockH.List)
			r.With(idemS.Middleware).Post("/stock/in", stockH.In)
			r.With(auth.RequireRoles("owner", "farm_manager")).Post("/stock/{id}/wastage", stockH.Wastage)
			r.Get("/items/match", lookupH.Match)

			// Owner-only settings.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireOwner)
				r.Post("/items", catH.CreateItem)
				r.Patch("/items/{id}", priceH.PatchItem)
				r.Delete("/items/{id}", priceH.DeleteItem)

				r.Patch("/shops/current", profH.PatchShop)
				r.Put("/shops/current/categories", profH.PutCategories)
				r.Put("/locations", profH.PutLocations)
				r.Put("/offer", profH.PutOffer)
				r.Post("/staff/invite", profH.Invite)
				r.Patch("/staff/{id}", profH.PatchStaff)
				r.Delete("/staff/{id}", profH.RemoveStaff)
				r.Post("/devices/whatsapp/test", profH.TestWhatsApp)
				r.Get("/plan", profH.GetPlan)
				r.Post("/plan/upgrade", profH.Upgrade)
				r.Post("/backups", profH.CreateBackup)
				r.Get("/backups/latest", profH.LatestBackup)
				r.Post("/exports", profH.CreateExport)
				r.Get("/exports/{id}", profH.GetExport)
				r.Put("/prices/today", catH.SetToday)
				r.Patch("/stock/{id}", stockH.PatchReorder)
				r.Patch("/setup/steps/{id}", setupH.Patch)
				r.Put("/rewards", rewardH.Put)
				r.Post("/rewards/items", rewardH.CreateItem)
				r.Delete("/rewards/items/{id}", rewardH.DeleteItem)
				r.Post("/categories", priceH.CreateCategory)
				r.Post("/prices/items/bulk", priceH.BulkCreate)
				r.Delete("/prices/items/{id}", priceH.DeleteItem)
			})
		})
	})

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("Mo Hisab API listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
