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

var version = "1.0.0"
var fileMutex sync.Mutex

type RequestBody struct {
	Category    string                 `json:"category"`
	SubCategory string                 `json:"subCategory"`
	Page        int                    `json:"page"`
	Filters     map[string]interface{} `json:"filters"`
}

func createRequestBody(videoIDs []int) (*RequestBody, error) {
	return &RequestBody{
		Category:    "all",
		SubCategory: "all",
		Page:        1,
		Filters: map[string]interface{}{
			"maxResults":   1,
			"collectionId": videoIDs,
		},
	}, nil
}

func sendPostRequest(videoIDs []int, client *warc.CustomHTTPClient, wg *sync.WaitGroup, responseCh chan<- string) {
	defer wg.Done()

	body, err := createRequestBody(videoIDs)
	if err != nil {
		fmt.Println("Error creating request body:", err)
		return
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		fmt.Println("Error marshalling JSON:", err)
		return
	}

	req, err := http.NewRequest("POST", "https://veoh.com/list/category/collections", bytes.NewBuffer(jsonData))
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
		Success     bool `json:"success"`
		Collections []struct {
			PermalinkId string `json:"permalinkId"`
		} `json:"collections"`
	}

	// Add this code to parse the response body and write permalinkIds to permalinks.txt
	if len(respBody) == 0 {
		fmt.Println("Error: response body is empty")
		return
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		fmt.Println("Error unmarshalling response:", err)
		return
	}

	permalinkFile, err := os.OpenFile("permalinks.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer permalinkFile.Close()

	for _, collection := range response.Collections {
		if _, err := permalinkFile.WriteString(fmt.Sprintf("%s\n", collection.PermalinkId)); err != nil {
			fmt.Println("Error writing to file:", err)
			return
		}
	}

	file, err := os.OpenFile("checked.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	for _, id := range videoIDs {
		if _, err := file.WriteString(fmt.Sprintf("%d\n", id)); err != nil {
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
	rotatorSettings.WarcinfoContent.Set("software", fmt.Sprintf("veoh-bruteforcer/%s", version))
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
	print("done!")

	idRange := *end - *start + 1
	numRequests := (idRange + *chunks - 1) / *chunks
	var wg sync.WaitGroup
	responseCh := make(chan string, numRequests)
	sem := make(chan struct{}, *threads)

	for i := 0; i < numRequests; i++ {
		startID := *start + i**chunks
		endID := startID + *chunks
		if endID > *end {
			endID = *end + 1
		}
		videoIDs := make([]int, endID-startID)
		for j := range videoIDs {
			videoIDs[j] = startID + j
		}

		wg.Add(1)
		sem <- struct{}{} // Limit the number of concurrent requests
		go func(ids []int) {
			defer func() { <-sem }()
			sendPostRequest(ids, client, &wg, responseCh)
		}(videoIDs)
	}

	go func() {
		wg.Wait()
		close(responseCh)
	}()

	done := make(chan struct{})
	go func() {
		for resp := range responseCh {
			fmt.Println("Response:", resp)
		}
		close(done)
	}()

	<-done // Wait for the response processing to complete
	fmt.Println("All requests done!")
	client.Close()

}
