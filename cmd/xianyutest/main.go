// xianyutest explores XianYu login flow with stealth mode.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

func main() {
	// Launch browser with stealth-friendly flags.
	u := launcher.New().
		Headless(false). // visible window so user can interact if needed
		Set("disable-blink-features", "AutomationControlled").
		MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	// Create page with stealth.
	page := stealth.MustPage(browser)
	defer page.MustClose()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("1. Navigating to XianYu...")
	if err := page.Context(ctx).Navigate("https://www.goofish.com"); err != nil {
		log.Fatalf("navigate: %v", err)
	}

	page.MustWaitLoad()
	time.Sleep(5 * time.Second) // wait for JS to render

	info, _ := page.Info()
	fmt.Printf("   URL: %s\n", info.URL)
	fmt.Printf("   Title: %s\n", info.Title)

	// Take screenshot of the page.
	saveScreenshot(page, "xianyu-stealth.png")
	fmt.Println("   Screenshot: xianyu-stealth.png")

	// Try to find login button and click it.
	fmt.Println("\n2. Looking for login button...")
	loginBtn, err := page.ElementR("span", "登录")
	if err != nil {
		// Try common login selectors.
		selectors := []string{
			"a[href*='login']",
			".login-btn",
			"[class*='login']",
			"button",
		}
		for _, s := range selectors {
			el, err := page.Element(s)
			if err == nil {
				text := el.MustText()
				fmt.Printf("   Found element: %s -> text=%q\n", s, text)
			}
		}
	} else {
		fmt.Printf("   Found login button: %s\n", loginBtn.MustText())
		if err := loginBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
			log.Printf("click login: %v", err)
		}
		time.Sleep(5 * time.Second)
		saveScreenshot(page, "xianyu-login.png")
		fmt.Println("   Screenshot after login click: xianyu-login.png")
	}

	// Try direct login URL.
	fmt.Println("\n3. Trying direct login URL...")
	if err := page.Context(ctx).Navigate("https://login.taobao.com/member/login.jhtml?redirectURL=https://www.goofish.com"); err != nil {
		log.Printf("navigate login: %v", err)
	}
	page.MustWaitLoad()
	time.Sleep(5 * time.Second)
	info, _ = page.Info()
	fmt.Printf("   URL: %s\n", info.URL)
	fmt.Printf("   Title: %s\n", info.Title)
	saveScreenshot(page, "xianyu-taobao-login.png")
	fmt.Println("   Screenshot: xianyu-taobao-login.png")

	fmt.Println("\nDone. Check the screenshot files.")
}

func saveScreenshot(page *rod.Page, filename string) {
	png, err := page.Screenshot(true, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		log.Printf("screenshot: %v", err)
		return
	}
	if err := os.WriteFile(filename, png, 0644); err != nil {
		log.Printf("write: %v", err)
	}
}
