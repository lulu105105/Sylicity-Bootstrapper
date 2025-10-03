package main

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"syscall"

	"nekoriabootstrapper/themecode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	versionURL      = "https://setup.kroner.lol/version" // currently placeholder
	downloadURLBase = "https://setup.kroner.lol/" // currently placeholder
	appName         = "Nekoria"
	protocolScheme  = "nekoria-player" // for later
	authURLDefault  = "https://www.kroner.lol/Login/Negotiate.ashx"
	clientVersionsAPI = "https://clientversions.kroner.lol/v1/client-versions" // currently placeholder
)

type greenTheme struct {
	fyne.Theme
}

func (m *greenTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNamePrimary {
		if variant == theme.VariantDark {
			return color.NRGBA{R: 0x6B, G: 0xE4, B: 0x8F, A: 0xFF}
		}
		return color.NRGBA{R: 0x27, G: 0xB5, B: 0x3D, A: 0xFF}
	}
	return m.Theme.Color(name, variant)
}

type LaunchOptions struct {
	LaunchMode string
	Script     string
	AuthTicket string
	ClientYear string
}

type ClientInfo struct {
	Hash string `json:"hash"`
	URL  string `json:"url"`
}

type ClientVersionsResponse struct {
	Clients map[string]ClientInfo `json:"clients"`
}

func main() {
	launchOpts, err := parseLaunchOptions()
	if err != nil {
		fmt.Printf("Argument parsing error: %v\n", err)
	}

	myApp := app.New()
	myWindow := myApp.NewWindow("Nekoria Installer")

	detectedVariant := themecode.DetectSystemTheme()
	var baseTheme fyne.Theme
	if detectedVariant == theme.VariantLight {
		baseTheme = theme.LightTheme()
	} else {
		baseTheme = theme.DarkTheme()
	}
	myApp.Settings().SetTheme(&greenTheme{Theme: baseTheme})

	logoImage := canvas.NewImageFromFile("Nekoria.png")
	logoImage.FillMode = canvas.ImageFillContain
	logoImage.SetMinSize(fyne.NewSize(96, 96))

	statusLabel := widget.NewLabel("Preparing to install...")
	statusLabel.Alignment = fyne.TextAlignCenter

	customLoader, track, chunkToAnimate := createCustomLoader()
	cancelButton := widget.NewButton("Cancel", func() { myApp.Quit() })

	verticalSpacer := canvas.NewRectangle(color.Transparent)
	verticalSpacer.SetMinSize(fyne.NewSize(0, 12))

	content := container.NewVBox(
		layout.NewSpacer(),
		container.NewCenter(logoImage),
		statusLabel,
		customLoader,
		verticalSpacer,
		container.NewCenter(cancelButton),
		layout.NewSpacer(),
	)

	myWindow.SetContent(content)
	myWindow.Resize(fyne.NewSize(440, 280))
	myWindow.SetFixedSize(true)
	myWindow.CenterOnScreen()

	loaderWidth := float32(440) - theme.Padding()*4
	go runInstallerLogic(launchOpts, statusLabel, customLoader, track, chunkToAnimate, loaderWidth, cancelButton, myWindow)

	myWindow.ShowAndRun()
}

func createCustomLoader() (fyne.CanvasObject, *canvas.Rectangle, *canvas.Rectangle) {
	track := canvas.NewRectangle(theme.DisabledColor())
	chunk := canvas.NewRectangle(theme.PrimaryColor())
	loader := container.NewWithoutLayout(track, chunk)
	return loader, track, chunk
}

func animateIndeterminate(chunk *canvas.Rectangle, totalWidth float32, stop chan bool, wg *sync.WaitGroup) {
	defer wg.Done()
	chunkWidth := float32(80)
	chunk.Resize(fyne.NewSize(chunkWidth, 8))
	var xPos float32 = -chunkWidth
	for {
		select {
		case <-stop:
			return
		default:
			xPos += 4
			if xPos > totalWidth {
				xPos = -chunkWidth
			}
			chunk.Move(fyne.NewPos(xPos, 0))
			time.Sleep(16 * time.Millisecond)
		}
	}
}

func parseLaunchOptions() (LaunchOptions, error) {
	opts := LaunchOptions{LaunchMode: "install"}

	if len(os.Args) <= 1 {
		return opts, nil
	}

	firstArg := strings.Trim(os.Args[1], `"'`)

	if strings.HasPrefix(firstArg, protocolScheme+":") {
		normalized := firstArg
		if !strings.HasPrefix(firstArg, protocolScheme+"://") {
			normalized = strings.Replace(firstArg, protocolScheme+":", protocolScheme+"://", 1)
		}
		return parseProtocolArgs(normalized)
	}

	if firstArg == "-play" {
		return parseCommandLineArgs(os.Args)
	}

	return opts, nil
}

func parseProtocolArgs(arg string) (LaunchOptions, error) {
	opts := LaunchOptions{}
	trimmed := strings.TrimPrefix(arg, protocolScheme+"://")

	parts := strings.Split(trimmed, "+")
	for _, part := range parts {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) == 2 {
			key, value := kv[0], kv[1]
			switch key {
			case "launchmode":
				opts.LaunchMode = value
			case "placelauncherurl":
				decodedValue, err := url.QueryUnescape(value)
				if err != nil {
					return opts, fmt.Errorf("invalid placelauncherurl: %w", err)
				}
				opts.Script = decodedValue
			case "gameinfo":
				opts.AuthTicket = value
			case "clientyear":
				opts.ClientYear = value
			}
		}
	}

	if opts.LaunchMode != "play" {
		return opts, fmt.Errorf("unsupported launch mode: %s", opts.LaunchMode)
	}

	return opts, nil
}

func parseCommandLineArgs(args []string) (LaunchOptions, error) {
	opts := LaunchOptions{LaunchMode: "play"}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-play":
			opts.LaunchMode = "play"
		case "-script":
			if i+1 < len(args) {
				opts.Script = args[i+1]
				i++
			}
		case "-ticket":
			if i+1 < len(args) {
				opts.AuthTicket = args[i+1]
				i++
			}
		case "-clientyear":
			if i+1 < len(args) {
				opts.ClientYear = args[i+1]
				i++
			}
		}
	}
	return opts, nil
}

func getClientVersions() (map[string]ClientInfo, error) {
	resp, err := http.Get(clientVersionsAPI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status: %s", resp.Status)
	}

	var data ClientVersionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return data.Clients, nil
}

func getSHA1Hash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha1.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
// TODO: add an actual check
func checkAndUpdateClients(appDir string, onProgress func(string, float32), forceInstall bool) error {
	clients, err := getClientVersions()
	if err != nil {
		return fmt.Errorf("failed to fetch client versions: %w", err)
	}

	versionsDir := filepath.Join(appDir, "Versions")
	if err := os.MkdirAll(versionsDir, 0755); err != nil {
		return err
	}

	for year, info := range clients {
		clientDir := filepath.Join(versionsDir, fmt.Sprintf("Client%s", year))
		exePath := filepath.Join(clientDir, "NekoriaPlayerBeta.exe")

		needsInstall := forceInstall
		if !forceInstall {
			if _, err := os.Stat(exePath); os.IsNotExist(err) {
				needsInstall = true
			}
		}

		if !needsInstall {
			fmt.Printf("Client %s exists, skipping (forceInstall=false).\n", year)
			continue
		}

		fmt.Printf("Installing client %s...\n", year)
		onProgress(fmt.Sprintf("Installing client %s...", year), 0)

		if err := os.RemoveAll(clientDir); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(clientDir, 0755); err != nil {
			return err
		}

		zipPath, err := downloadClientZip(info.URL, func(p float32) {
			onProgress(fmt.Sprintf("Downloading client %s...", year), p)
		})
		if err != nil {
			return fmt.Errorf("failed to download client %s: %w", year, err)
		}

		if err := unzipToDir(zipPath, clientDir); err != nil {
			os.Remove(zipPath)
			return fmt.Errorf("failed to extract client %s: %w", year, err)
		}
		os.Remove(zipPath)

		onProgress(fmt.Sprintf("Installed client %s", year), 1)
	}

	return nil
}
// TODO: improve this
func downloadSpecificClient(appDir, clientYear string, onProgress func(string, float32), forceInstall bool) error {
	clients, err := getClientVersions()
	if err != nil {
		return fmt.Errorf("failed to fetch client versions: %w", err)
	}

	info, exists := clients[clientYear]
	if !exists {
		return fmt.Errorf("client year %s not available", clientYear)
	}

	versionsDir := filepath.Join(appDir, "Versions")
	clientDir := filepath.Join(versionsDir, fmt.Sprintf("Client%s", clientYear))
	exePath := filepath.Join(clientDir, "NekoriaPlayerBeta.exe")

	needsInstall := forceInstall
	if !forceInstall {
		if _, err := os.Stat(exePath); os.IsNotExist(err) {
			needsInstall = true
		}
	}

	if !needsInstall {
		fmt.Printf("Client %s exists, skipping (forceInstall=false).\n", clientYear)
		return nil
	}

	fmt.Printf("Installing client %s...\n", clientYear)
	onProgress(fmt.Sprintf("Installing client %s...", clientYear), 0)

	if err := os.RemoveAll(clientDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(clientDir, 0755); err != nil {
		return err
	}

	zipPath, err := downloadClientZip(info.URL, func(p float32) {
		onProgress(fmt.Sprintf("Downloading client %s...", clientYear), p)
	})
	if err != nil {
		return fmt.Errorf("failed to download client: %w", err)
	}

	if err := unzipToDir(zipPath, clientDir); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("failed to extract client: %w", err)
	}
	os.Remove(zipPath)

	onProgress(fmt.Sprintf("Installed client %s", clientYear), 1)
	return nil
}


func downloadClientZip(urlStr string, onProgress func(float32)) (string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Roblox/WinInet")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.CreateTemp("", "client-*.zip")
	if err != nil {
		return "", err
	}
	defer out.Close()

	writer := &ProgressWriter{
		Total:      resp.ContentLength,
		File:       out,
		OnProgress: onProgress,
	}

	_, err = io.Copy(writer, resp.Body)
	return out.Name(), err
}

func unzipToDir(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func runInstallerLogic(opts LaunchOptions, label *widget.Label, cLoader fyne.CanvasObject, track *canvas.Rectangle, chunk *canvas.Rectangle, loaderWidth float32, btn *widget.Button, win fyne.Window) {
	var wg sync.WaitGroup
	stopAnimation := make(chan bool, 1)
	isAnimating := true

	track.Move(fyne.NewPos(0, 0))
	track.Resize(fyne.NewSize(loaderWidth, 8))
	track.Refresh()

	wg.Add(1)
	go animateIndeterminate(chunk, loaderWidth, stopAnimation, &wg)

	appDir, err := getAppDir()
	if err != nil {
		label.SetText(fmt.Sprintf("Error: %v", err))
		return
	}

	label.SetText("Checking for client installation...")

	var progressMu sync.Mutex
	progressCallback := func(msg string, progress float32) {
		progressMu.Lock()
		defer progressMu.Unlock()

		if isAnimating {
			select {
			case stopAnimation <- true:
			default:
			}
			wg.Wait()
			isAnimating = false
		}

		label.SetText(msg)
		track.Move(fyne.NewPos(0, 0))
		track.Resize(fyne.NewSize(loaderWidth, 8))
		track.Refresh()
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		chunk.Resize(fyne.NewSize(loaderWidth*progress, 8))
		chunk.Move(fyne.NewPos(0, 0))
		chunk.Refresh()
	}

	forceInstall := opts.LaunchMode != "play"

	if opts.ClientYear != "" {
		if err := downloadSpecificClient(appDir, opts.ClientYear, progressCallback, forceInstall); err != nil {
			label.SetText(fmt.Sprintf("Failed to download client: %v", err))
			return
		}
	} else {
		if err := checkAndUpdateClients(appDir, progressCallback, forceInstall); err != nil {
			label.SetText(fmt.Sprintf("Failed to update clients: %v", err))
			return
		}
	}

	if !isAnimating {
		isAnimating = true
		stopAnimation = make(chan bool, 1)
		track.Move(fyne.NewPos(0, 0))
		track.Resize(fyne.NewSize(loaderWidth, 8))
		track.Refresh()
		wg.Add(1)
		go animateIndeterminate(chunk, loaderWidth, stopAnimation, &wg)
	}

	if err := createDesktopFile(); err != nil {
		fmt.Printf("Warning: could not create desktop file: %v\n", err)
	}

	if opts.LaunchMode == "play" {
		label.SetText("Starting Nekoria...")
		if err := launchClient(appDir, opts); err != nil {
			label.SetText(fmt.Sprintf("Failed to launch: %v", err))
			if isAnimating {
				select {
				case stopAnimation <- true:
				default:
				}
				wg.Wait()
				isAnimating = false
			}
			return
		}
	}

	if isAnimating && opts.LaunchMode != "play" {
		select {
		case stopAnimation <- true:
		default:
		}
		wg.Wait()
		isAnimating = false
	}

	if opts.LaunchMode != "play" {
		label.SetText("Nekoria is ready!")
		cLoader.Hide()
		btn.SetText("Finish")
		btn.OnTapped = func() { win.Close() }
		btn.Refresh()
	} else {
		go func() {
			time.Sleep(5 * time.Second)
			win.Close()
		}()
	}
}

func getAppDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appPath := filepath.Join(configDir, appName)
	return appPath, os.MkdirAll(appPath, 0755)
}

type ProgressWriter struct {
	Total      int64
	Written    int64
	File       *os.File
	OnProgress func(float32)
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n, err := pw.File.Write(p)
	if err == nil {
		pw.Written += int64(n)
		if pw.Total > 0 {
			progress := float32(pw.Written) / float32(pw.Total)
			pw.OnProgress(progress)
		}
	}
	return n, err
}


func createDesktopFile() error {
	if runtime.GOOS != "linux" {
		return nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	desktopFilePath := filepath.Join(homeDir, ".local", "share", "applications", "nekoria-installer.desktop")

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	// TODO: embed the file inside the launcher
	iconPath := filepath.Join(filepath.Dir(exePath), "Nekoria.png")

	desktopContent := fmt.Sprintf(`[Desktop Entry]
Name=Nekoria
Comment=Nekoria Game Launcher
Exec="%s"
Icon=%s
Terminal=false
Type=Application
Categories=Game;
`, exePath, iconPath)

	return os.WriteFile(desktopFilePath, []byte(desktopContent), 0644)
}

func launchClient(appDir string, opts LaunchOptions) error {
    clientYear := opts.ClientYear
    if clientYear == "" {
        clientYear = "2017"
    }

    clientFolder := fmt.Sprintf("Client%s", clientYear)
    exeName := "NekoriaPlayerBeta.exe"
    exePath := filepath.Join(appDir, "Versions", clientFolder, exeName)

    if _, err := os.Stat(exePath); os.IsNotExist(err) {
        return fmt.Errorf("client executable not found: %s", exePath)
    }

    var args []string
    if opts.LaunchMode == "play" {
        args = append(args, "--play")
        args = append(args, "--authenticationUrl", authURLDefault)
        args = append(args, "--authenticationTicket", opts.AuthTicket)
        args = append(args, "--joinScriptUrl", opts.Script)
    }

    fmt.Printf("Launching client with command:\n%s %s\n", exePath, strings.Join(args, " "))

    cmd := exec.Command(exePath, args...)
	// TODO: windows support for this call, will do later, as it's not top priority right now.
        cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

        devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
        if err == nil {
            cmd.Stdin = devnull
            cmd.Stdout = devnull
            cmd.Stderr = devnull
            defer devnull.Close()
       }
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("failed to start client: %w", err)
    }

    if cmd.Process == nil {
        return fmt.Errorf("process started but process handle is nil")
    }

    if err := cmd.Process.Release(); err != nil {
        return fmt.Errorf("started client but failed to detach (release): %w", err)
    }

    return nil
}
