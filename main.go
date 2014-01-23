package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path"
	"sync"
	//"sort"
	"strings"
	"encoding/xml"
)

type CardInfo struct {
	ID      string
	Set     string
	SetID   string
	Name    string
	Quality uint
}

type AssetSource struct {
	Quality    uint
	ComposeURL func(info CardInfo) string
}
type AssetList []AssetSource
var sources AssetList = AssetList{
	{
		Quality: 125400,
		ComposeURL: func(info CardInfo) string {
			startIdx := len(info.ID) - 5
			buffer := bytes.NewBufferString("http://netrunnerdb.com/web/bundles/netrunnerdbcards/images/cards/en/")
			buffer.WriteString(info.ID[startIdx:])
			buffer.WriteString(".png")
			return buffer.String()
		},
	},
	{
		Quality: 58870,
		ComposeURL: func(info CardInfo) string {
			buffer := bytes.NewBufferString("http://www.cardgamedb.com/forums/uploads/an/")
			buffer.WriteString("ffg_")
			buffer.WriteString(strings.Replace(strings.ToLower(info.Name), " ", "-", -1))
			buffer.WriteString("-")
			buffer.WriteString(strings.Replace(strings.ToLower(info.Set), " ", "-", -1))
			buffer.WriteString(".png")
			return buffer.String()
		},
	},
}
var pngSig []byte = []byte{'\x89', '\x50', '\x4E', '\x47', '\x0D', '\x0A', '\x1A', '\x0A'}

const (
	octgnGameId string = "0f38e453-26df-4c04-9d67-6d43de939c77"
	markerSetId string = "21bf9e05-fb23-4b1d-b89a-398f671f5999"
	consumeThreads int = 1
	chanSize int = 60
)
var wGroup sync.WaitGroup
var wChan = make(chan Task, chanSize)

type NetSet struct {
	XMLName xml.Name `xml:"set"`
	Name    string   `xml:"name,attr"`
	ID      string   `xml:"id,attr"`
	Cards   NetCards `xml:"cards"`
}
type NetCards struct {
	Cards []NetCard `xml:"card"`
}
type NetCard struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

type Task struct {
	Target string
	Card   CardInfo
}

//Parse a set xml file and return slice of cards
func parseSetXML(xmlPath string) (results []CardInfo, err error) {
	//read data from xml file
	data, err := ioutil.ReadFile(xmlPath)
	if err != nil {
		return results, err
	}

	//parse that xml data
	v := new(NetSet)
	err = xml.Unmarshal(data, &v)
	if err != nil {
		return results,err
	}
	
	//parse out all the cards
	for _, cur := range v.Cards.Cards {
		newItem := CardInfo{
			ID:    cur.ID,
			Name:  cur.Name,
			Set:   v.Name,
			SetID: v.ID,
		}
		results = append(results, newItem)
	}
	return
}

//Get important OCTGN Netrunner directory paths
func getPaths() (setPath string, imgPath string) {
	curUser, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	installPath := path.Join(curUser.HomeDir, "Documents", "OCTGN")
	setPath = path.Join(installPath, "GameDatabase", octgnGameId, "Sets")
	imgPath = path.Join(installPath, "ImageDatabase", octgnGameId, "Sets")
	return
}

//Download images for work in queue
func consumer() {
	defer wGroup.Done()
	for curTask := range wChan {
		for _,curAsset := range sources{
			//Cycle through sources so long as resolution quality of source is better than
			//current card's imgQuality.  No card == quality 0.
			
			if curTask.Card.Quality >= curAsset.Quality{
				//No more good sources, abort current task
				break
			}
			
			url := curAsset.ComposeURL(curTask.Card)
			fmt.Printf("Attempting download: %s - %s\n",curTask.Card.Set,curTask.Card.Name)
			if err := doDownload(url,curTask.Target); err == nil{
				//Download was a success, can abort current task
				break
			} else{
				//Download failed, keep looping for next source
				fmt.Printf("Failed Get: %s - %s\n\t%s\n",curTask.Card.Set, curTask.Card.Name,err)
			}
		}
	}
}

//Download contents of url to target path
func doDownload(url string, target string) (err error){
	//Get the url contents
	resp, err := http.Get(url)
	defer resp.Body.Close()
	if err !=nil{
		return err
	}
	//Open file handle
	target = "TMP"//DEBUG
	out, err := os.Create(target)
	defer out.Close()
	if err !=nil{
		return err
	}
	fmt.Println(resp.Body)
	//Copy response into file
	_, err = io.Copy(out, resp.Body)
	if err !=nil{
		return err
	}
	return
}

//Loads wChan with list of Netrunner cards to consider downloading
func producer(){
	setPath, imgPath := getPaths()
	
	//get list of set directories
	setDirs, err := ioutil.ReadDir(setPath)
	if err != nil {
		log.Fatal(err)
	}
	
	for _, curSet := range setDirs{
	
		//Skip over the marker set
		if curSet.Name() == markerSetId {
			continue
		}
		
		//Get the collection of cards from the set
		setFile := fmt.Sprintf("%s/%s/set.xml", setPath, curSet.Name())
		setColl, err := parseSetXML(setFile)
		if err != nil{
			fmt.Printf("Error loading set file: %s\n\t%s\n",setFile, err)
			continue
		}
		
		for _, curCard:= range setColl{
		
			//Get card quality
			curPath := path.Join(imgPath, curCard.SetID, "Cards", curCard.ID+ ".png")
			curCard.Quality = getPNGQuality(curPath)			
			//if best quality of source > card Quality, queue for downloads
			if curCard.Quality < sources[0].Quality{			
				wChan <- Task{curPath, curCard}
			}
		}
	}
}

func unpackUInt(bSlice []byte) (result uint32){
	//TODO: Endianness check??
	binary.Read(bytes.NewBuffer(bSlice), binary.BigEndian, &result)
	return
}

func getPNGQuality(curPath string) uint{
	fHandle, err := os.Open(curPath)	
	defer fHandle.Close()
	if err != nil {
		return 0
	}
	
	
	head := make([]byte, 24)
	count, err := fHandle.Read(head)	
	if err !=nil || count != len(head) || !bytes.Equal(head[0:8],pngSig){		
		return 0
	}
	
	quality := uint(unpackUInt(head[16:20]) * unpackUInt(head[20:24]))
	return quality	
}

func main() {
	for i := 0; i < consumeThreads; i++ {
		wGroup.Add(1)
		go consumer()
	}
	producer()	
	close(wChan)
	wGroup.Wait()
}
