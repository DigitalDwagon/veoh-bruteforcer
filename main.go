package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/CorentinB/warc"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sync"
)

var version = "1.0.3"
var fileMutex sync.Mutex

type RequestBody struct {
	Q               string        `json:"q"`
	Filters         []interface{} `json:"filters"`
	LegacyLibFilter bool          `json:"legacyLibFilter"`
	MainLibFilter   bool          `json:"mainLibFilter"`
	AnyFilters      []interface{} `json:"anyFilters"`
	Sort            string        `json:"sort"`
	SortDirection   int           `json:"sortDirection"`
	Size            int           `json:"size"`
	Skip            int           `json:"skip"`
}

func createRequestBody(chunkSize, offset int) (*RequestBody, error) {
	return &RequestBody{
		Q:               "",
		Filters:         []interface{}{},
		LegacyLibFilter: true,
		MainLibFilter:   true,
		AnyFilters:      []interface{}{},
		Sort:            "",
		SortDirection:   1,
		Size:            chunkSize,
		Skip:            offset,
	}, nil
}

func sendPostRequest(chunkSize int, offset int, client *warc.CustomHTTPClient, wg *sync.WaitGroup, responseCh chan<- string) {
	defer wg.Done()
	fmt.Println("Offset:", offset)

	body, err := createRequestBody(chunkSize, offset)
	if err != nil {
		fmt.Println("Error creating request body:", err)
		return
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		fmt.Println("Error marshalling JSON:", err)
		return
	}

	req, err := http.NewRequest("POST", "https://adams-search.nrc.gov/api/search", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Println("Error creating request:", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Csrf-Token", "2feRBVVYXqqt5boLqsyFfIAX5H5YsyrcI9tZWbc5")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending request:", err)
		return
	}
	defer resp.Body.Close()

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return
	}
	fmt.Println("Response:", string(respBody))

	// Write each checked ID to checked.txt in a thread-safe manner
	fileMutex.Lock()
	defer fileMutex.Unlock()

	// Add this struct to parse the response body
	var response struct {
		Count   int `json:"count"`
		Results []struct {
			Document struct {
				URL string `json:"Url"`
			} `json:"document"`
		} `json:"results"`
		Facets struct {
			Keyword []struct {
				Value string `json:"value"`
				Count int    `json:"count"`
			} `json:"Keyword"`
		} `json:"facets"`
	}

	if len(respBody) == 0 {
		fmt.Println("Error: response body is empty")
		return
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		fmt.Println("Error unmarshalling response:", err)
		return
	}

	permalinkFile, err := os.OpenFile("discovered.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer permalinkFile.Close()

	for _, result := range response.Results {
		if _, err := permalinkFile.WriteString(fmt.Sprintf("%s\n", result.Document.URL)); err != nil {
			fmt.Println("Error writing to file:", err)
			return
		}
	}

	responseCh <- string(respBody)
}

func main() {

	chunks := flag.Int("chunks", 50, "Number of IDs per request")
	start := flag.Int("start", 0, "Starting ID")
	end := flag.Int("end", 100, "Ending ID")
	threads := flag.Int("threads", 5, "Number of concurrent requests")

	flag.Parse()

	if *end <= *start {
		fmt.Println("End ID must be greater than start ID")
		os.Exit(1)
	}

	var rotatorSettings = warc.NewRotatorSettings()

	rotatorSettings.OutputDirectory = path.Join(".", "warcs")
	rotatorSettings.Compression = "ZSTD"
	rotatorSettings.Prefix = "veoh-bruteforcer"
	rotatorSettings.WarcinfoContent.Set("software", fmt.Sprintf("bruteforcer/adams-search.nrc.gov/%s", version))
	rotatorSettings.WARCWriterPoolSize = 1
	rotatorSettings.WarcSize = float64(15360)
	rotatorSettings.WarcinfoContent.Set("operator", "DigitalDragon <warc@digitaldragon.dev>")
	rotatorSettings.WarcinfoContent.Set("X-Bruteforcer-Range", fmt.Sprintf("%d-%d", *start, *end))

	dedupeOptions := warc.DedupeOptions{LocalDedupe: true, SizeThreshold: 30}

	HTTPClientSettings := warc.HTTPClientSettings{
		RotatorSettings:     rotatorSettings,
		DedupeOptions:       dedupeOptions,
		DecompressBody:      true,
		SkipHTTPStatusCodes: []int{429},
		VerifyCerts:         true,
		TempDir:             path.Join(".", "tmp"),
		FullOnDisk:          false,
		RandomLocalIP:       false,
		DisableIPv4:         false,
		DisableIPv6:         false,
		IPv6AnyIP:           false,
	}

	var client, err = warc.NewWARCWritingHTTPClient(HTTPClientSettings)
	if err != nil {
		log.Fatal("Failed to start warc client!", err)
	}
	defer func(client *warc.CustomHTTPClient) {
		err := client.Close()
		if err != nil {
			log.Fatal("Failed to close warc client!", err.Error())
		}
	}(client)
	print("done!")

	var wg sync.WaitGroup
	responseCh := make(chan string, (*end-*start)/(*chunks))
	sem := make(chan struct{}, *threads)

	for i := *start; i < *end; i += *chunks {
		wg.Add(1)
		sem <- struct{}{} // Limit the number of concurrent requests
		go func(offset int) {
			defer func() { <-sem }()
			sendPostRequest(*chunks, offset, client, &wg, responseCh)
		}(i)
	}

	go func() {
		wg.Wait()
		close(responseCh)
	}()

	done := make(chan struct{})
	go func() {
		/*for resp := range responseCh {
			fmt.Println("Response:", resp)
		}*/
		close(done)
	}()

	<-done // Wait for the response processing to complete
	fmt.Println("All requests done!")

}
