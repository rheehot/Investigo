package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/corpix/uarand"
	color "github.com/fatih/color"
	chrm "github.com/tdh8316/Investigo/chrome"
	"golang.org/x/net/proxy"
)

const (
	userAgent       string = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.87 Safari/537.36"
	screenShotRes   string = "1024x768"
	torProxyAddress string = "socks5://127.0.0.1:9050"
)

var (
	maxGoroutines int = 8 // lower if taking screenshots, should be handled more dynamically
)

// Result of Investigo function
type Result struct {
	Usernane string
	Exist    bool
	Proxied  bool
	Site     string
	URL      string
	URLProbe string
	Link     string
	Err      bool
	ErrMsg   string
}

var (
	guard     = make(chan int, maxGoroutines)
	waitGroup = &sync.WaitGroup{}
	logger    = log.New(color.Output, "", 0)
	siteData  = map[string]SiteData{}
	options   struct {
		noColor        bool
		verbose        bool
		checkForUpdate bool
		runTest        bool
		useCustomdata  bool
		withTor        bool
		withScreenshot bool
	}
	dataFileName = "data.json"
)

// A SiteData struct for json datatype
type SiteData struct {
	ErrorType      string `json:"errorType"`
	ErrorMsg       string `json:"errorMsg"`
	URL            string `json:"url"`
	URLMain        string `json:"urlMain"`
	URLProbe       string `json:"urlProbe"`
	URLError       string `json:"errorUrl"`
	UsedUsername   string `json:"username_claimed"`
	UnusedUsername string `json:"username_unclaimed"`
	// RegexCheck string `json:"regexCheck"`
	// Rank int`json:"rank"`
}

// RequestError interface
type RequestError interface {
	Error() string
}

type counter struct {
	n int32
}

func (c *counter) Add() {
	atomic.AddInt32(&c.n, 1)
}

func (c *counter) Get() int {
	return int(atomic.LoadInt32(&c.n))
}

func parseArguments() []string {
	args := os.Args[1:]
	var argIndex int

	if help, _ := HasElement(args, "-h", "--help"); help  && !options.runTest {
		fmt.Print(
			`
usage: investigo [-h] [--no-color] [-v|--verbose] [-t|--tor] [--update] [--db FILENAME] USERNAME [USERNAMES...]
perform test: investigo [--test]

positional arguments:
	USERNAMES             one or more usernames to investigate

optional arguments:
	-h, --help            show this help message and exit
	-v, --verbose         output sites which is username was not found
	-s, --screenshot      take a screenshot of each matched urls
	-t, --tor             use tor proxy (default: ` + torProxyAddress + `)
	--no-color            disable colored stdout output
	--update              update datebase from Sherlock repository
	--db                  use custom database
`,
		)
		os.Exit(0)
	}

	if len(args) < 1 {
		fmt.Println("WARNING: You executed Investigo without arguments. Use `-h` flag if you need help.")
		var _usernames string
		scanner := bufio.NewScanner(os.Stdin)
		fmt.Printf("Input username to investigate:")
		scanner.Scan()
		_usernames = scanner.Text()
		return _usernames
	}

	options.noColor, argIndex = HasElement(args, "--no-color")
	if options.noColor {
		logger = log.New(os.Stdout, "", 0)
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.withTor, argIndex = HasElement(args, "-t", "--tor")
	if options.withTor {
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.withScreenshot, argIndex = HasElement(args, "-s", "--screenshot")
	if options.withScreenshot {
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.runTest, argIndex = HasElement(args, "--test")
	if options.runTest {
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.verbose, argIndex = HasElement(args, "-v", "--verbose")
	if options.verbose {
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.checkForUpdate, argIndex = HasElement(args, "--update")
	if options.checkForUpdate {
		args = append(args[:argIndex], args[argIndex+1:]...)
	}

	options.useCustomdata, argIndex = HasElement(args, "--db")
	if options.useCustomdata {
		dataFileName = args[argIndex+1]
		dataFile, err := os.Open(dataFileName)
		defer dataFile.Close()
		if err != nil {
			panic("Failed to read \"" + dataFileName + "\" Aborted.")
		}
		args = append(args[:argIndex], args[argIndex+2:]...)
	}

	return args
}

// Initialize sites not included in Sherlock
func initializeExtraSiteData() {
	siteData["Pornhub"] = SiteData{
		ErrorType: "status_code",
		URLMain:   "https://www.pornhub.com/",
		URL:       "https://www.pornhub.com/users/{}",
	}
	siteData["NAVER"] = SiteData{
		ErrorType: "status_code",
		URLMain:   "https://www.naver.com/",
		URL:       "https://blog.naver.com/{}",
	}
	siteData["xvideos"] = SiteData{
		ErrorType: "status_code",
		URLMain:   "https://xvideos.com/",
		URL:       "https://xvideos.com/profiles/{}",
	}
}

func main() {
	fmt.Println("Investigo - Investigate User Across Social Networks.")

	// Parse command-line arguments
	usernames := parseArguments()

	// Loads site data from sherlock database and assign to a variable.
	initializeSiteData(options.checkForUpdate)

	if options.runTest {
		test()
		os.Exit(0)
	}

	// Loads extra site data
	initializeExtraSiteData()

	for _, username := range usernames {
		if options.noColor {
			fmt.Printf("\nInvestigating %s on:\n", username)
		} else {
			fmt.Fprintf(color.Output, "Investigating %s on:\n", color.HiGreenString(username))
		}
		waitGroup.Add(len(siteData))
		for site := range siteData {
			guard <- 1
			go func(site string) {
				defer waitGroup.Done()
				res := Investigo(username, site, siteData[site])
				WriteResult(res)
				<-guard
			}(site)
		}
		waitGroup.Wait()
	}

	return
}

func initializeSiteData(forceUpdate bool) {
	jsonFile, err := os.Open(dataFileName)
	if err != nil || forceUpdate {
		if err != nil {
			if options.noColor {
				fmt.Printf(
					"[!] Cannot open database \"%s\"\n",
					dataFileName,
				)
			} else {
				fmt.Fprintf(
					color.Output,
					"[%s] Cannot open database \"%s\"\n",
					color.HiRedString("!"), (dataFileName),
				)
			}
		}
		if options.noColor {
			fmt.Printf(
				"%s Update database: %s",
				("[!]"),
				("Downloading..."),
			)
		} else {
			fmt.Fprintf(
				color.Output,
				"[%s] Update database: %s",
				color.HiBlueString("!"),
				color.HiYellowString("Downloading..."),
			)
		}

		if forceUpdate {
			jsonFile.Close()
		}

		r, err := Request("https://raw.githubusercontent.com/sherlock-project/sherlock/master/data.json")
		if err != nil || r.StatusCode != 200 {
			if options.noColor {
				fmt.Printf(" [%s]\n", ("Failed"))
			} else {
				fmt.Fprintf(color.Output, " [%s]\n", color.HiRedString("Failed"))
			}
			panic("Failed to update database.\n" + err.Error())
		} else {
			defer r.Body.Close()
		}
		if _, err := os.Stat(dataFileName); !os.IsNotExist(err) {
			if err = os.Remove(dataFileName); err != nil {
				panic(err)
			}
		}
		_updateFile, _ := os.OpenFile(dataFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if _, err := _updateFile.WriteString(ReadResponseBody(r)); err != nil {
			if options.noColor {
				fmt.Printf("Failed to update data.\n")
			} else {
				fmt.Fprintf(color.Output, color.RedString("Failed to update data.\n"))
			}
			panic(err)
		}

		_updateFile.Close()
		jsonFile, _ = os.Open(dataFileName)

		if options.noColor {
			fmt.Println(" [Done]")
		} else {
			fmt.Fprintf(color.Output, " [%s]\n", color.GreenString("Done"))
		}
	}

	defer jsonFile.Close()

	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		panic("Error while read " + dataFileName)
	} else {
		json.Unmarshal([]byte(byteValue), &siteData)
	}
	return
}

// Request makes an HTTP request
func Request(target string) (*http.Response, RequestError) {
	request, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	if options.withTor {
		tbProxyURL, err := url.Parse(torProxyAddress)
		if err != nil {
			return nil, err
		}
		tbDialer, err := proxy.FromURL(tbProxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		tbTransport := &http.Transport{
			Dial: tbDialer.Dial,
		}
		client.Transport = tbTransport
	}

	return client.Do(request)
}

// ReadResponseBody reads response body and return string
func ReadResponseBody(response *http.Response) string {
	bodyBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	return string(bodyBytes)
}

// HasElement reports whether elements is within array.
func HasElement(array []string, targets ...string) (bool, int) {
	for index, item := range array {
		for _, target := range targets {
			if item == target {
				return true, index
			}
		}
	}
	return false, -1
}

// Investigo investigate if username exists on social media.
func Investigo(username string, site string, data SiteData) Result {
	var u, urlProbe string
	var result Result

	// URL to be displayed
	u = strings.Replace(data.URL, "{}", username, 1)

	if data.URLProbe != "" {
		urlProbe = strings.Replace(data.URLProbe, "{}", username, 1)
	} else {
		urlProbe = u
	}

	r, err := Request(urlProbe)

	if err != nil {
		if r != nil {
			r.Body.Close()
		}
		return Result{
			Usernane: username,
			URL:      data.URL,
			URLProbe: data.URLProbe,
			Proxied:  options.withTor,
			Exist:    false,
			Site:     site,
			Err:      true,
			ErrMsg:   err.Error(),
		}
	}

	// check error types
	switch data.ErrorType {
	case "status_code":
		if r.StatusCode <= 300 || r.StatusCode < 200 {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				URLProbe: data.URLProbe,
				Proxied:  options.withTor,
				Exist:    true,
				Link:     u,
				Site:     site,
			}
		} else {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				Proxied:  options.withTor,
				Site:     site,
				Exist:    false,
				Err:      false,
			}
		}
	case "message":
		if !strings.Contains(ReadResponseBody(r), data.ErrorMsg) {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				URLProbe: data.URLProbe,
				Proxied:  options.withTor,
				Exist:    true,
				Link:     u,
				Site:     site,
			}
		} else {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				Proxied:  options.withTor,
				Site:     site,
				Exist:    false,
				Err:      false,
			}
		}
	case "response_url":
		// In the original Sherlock implementation,
		// the error type `response_url` works as `status_code`.
		if (r.StatusCode <= 300 || r.StatusCode < 200) && r.Request.URL.String() == u {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				URLProbe: data.URLProbe,
				Proxied:  options.withTor,
				Exist:    true,
				Link:     u,
				Site:     site,
			}
		} else {
			result = Result{
				Usernane: username,
				URL:      data.URL,
				Proxied:  options.withTor,
				Site:     site,
				Exist:    false,
				Err:      false,
			}
		}
	default:
		result = Result{
			Usernane: username,
			Proxied:  options.withTor,
			Exist:    false,
			Err:      true,
			ErrMsg:   "Unsupported error type `" + data.ErrorType + "`",
			Site:     site,
		}
	}

	r.Body.Close()

	if options.withScreenshot && result.Exist {
		urlParts, _ := url.Parse(urlProbe)
		folderPath := filepath.Join("screenshots", username)
		outputPath := filepath.Join(folderPath, urlParts.Host+".png")
		if err := os.MkdirAll(folderPath, 0755); err != nil {
			log.Fatal(err)
		}
		if err := getScreenshot(screenShotRes, urlProbe, outputPath); err != nil {
			log.Fatal(err)
		}
	}

	return result
}

// WriteResult writes investigation result to stdout and file
func WriteResult(result Result) {
	if options.noColor {
		if result.Exist {
			logger.Printf("[%s] %s: %s\n", ("+"), result.Site, result.Link)
		} else {
			if result.Err {
				logger.Printf("[%s] %s: ERROR: %s", ("!"), result.Site, (result.ErrMsg))
			} else if options.verbose {
				logger.Printf("[%s] %s: %s", ("-"), result.Site, ("Not Found!"))
			}
		}
	} else {
		if result.Exist {
			logger.Printf("[%s] %s: %s\n", color.HiGreenString("+"), color.HiWhiteString(result.Site), result.Link)
		} else {
			if result.Err {
				logger.Printf("[%s] %s: %s: %s", color.HiRedString("!"), result.Site, color.HiMagentaString("ERROR"), color.HiRedString(result.ErrMsg))
			} else if options.verbose {
				logger.Printf("[%s] %s: %s", color.HiRedString("-"), result.Site, color.HiYellowString("Not Found!"))
			}
		}
	}

	return
}

func getScreenshot(resolution, targetURL, outputPath string) error {
	chrome := &chrm.Chrome{
		Resolution:       resolution,
		ChromeTimeout:    60,
		ChromeTimeBudget: 60,
		UserAgent:        uarand.GetRandom(),
		// ScreenshotPath: "/opt/investigo/data",
	}
	// chrome.setLoggerStatus(false)
	chrome.Setup()
	u, err := url.ParseRequestURI(targetURL)
	if err != nil {
		return err
	}
	chrome.ScreenshotURL(u, outputPath)
	return nil
}

func test() {
	log.Println("Investigo is activated for checking site validity.")

	if options.withScreenshot {
		log.Println("Taking screenshot is not available in this sequence. Aborted.")
		return
	}

	tc := counter{}
	waitGroup.Add(len(siteData))
	for site := range siteData {
		guard <- 1
		go func(site string) {
			defer waitGroup.Done()
			var _currentContext = siteData[site]
			_usedUsername := _currentContext.UsedUsername
			_unusedUsername := _currentContext.UnusedUsername

			_resUsed := Investigo(_usedUsername, site, siteData[site])
			_resUnused := Investigo(_unusedUsername, site, siteData[site])

			if _resUsed.Exist && !_resUnused.Exist {
				// Works
			} else {
				// Not works
				var _errMsg string = ""
				if _resUsed.Err {
					_errMsg += fmt.Sprintf("[%s]", _resUsed.ErrMsg)
				}
				if _resUnused.Err {
					_errMsg += fmt.Sprintf("[%s]", _resUnused.ErrMsg)
				}

				if _errMsg != "" {
					if options.noColor {
						logger.Printf("[-] %s: %s %s", site, ("Failed with error"), _errMsg)
					} else {
						logger.Printf("[-] %s: %s %s", site, color.RedString("Failed with error"), _errMsg)
					}
				} else {
					if options.noColor {
						logger.Printf("[-] %s: %s", site, ("Failed"))
					} else {
						logger.Printf("[-] %s: %s", site, color.RedString("Failed"))
					}
				}

				tc.Add()
			}
			<-guard
		}(site)
	}
	waitGroup.Wait()

	if options.noColor {
		fmt.Println("[Done]")
	} else {
		fmt.Fprintf(color.Output, "[%s]\n", color.GreenString("Done"))
	}

	logger.Printf("\nThese %d sites are not compatible with the Sherlock database.\n"+
		"Please check https://github.com/tdh8316/Investigo/#to-fix-incompatible-sites", tc.Get())
}
