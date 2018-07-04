package main

import (
	"encoding/json"
	"fmt"
	"github.com/pborman/uuid"
	elastic "gopkg.in/olivere/elastic.v3"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type Location struct { //go中变量大写字母开头 相当于public可在函数外调用 小写字母开头相当于private.
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"` //`json:"lon"`是json Decoder时自动读取json做变量名的转换 因为json中命名习惯为小写字母开头
}

type Post struct {
	// `json:"user"` is for the json parsing of this User field. Otherwise, by default it's 'User'.
	User     string   `json:"user"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
}

const ( //go中定义常量的方法  类似final
	INDEX    = "around" //因为elasticSearch可以给不同的project用 index是用来区分的
	TYPE     = "post"
	DISTANCE = "200km"
	ES_URL   = "http://35.238.49.246:9200/"
)

func main() {

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Use the IndexExists service to check if a specified index exists.
	exists, err := client.IndexExists(INDEX).Do() //判断INDEX在不在
	if err != nil {
		panic(err)
	}
	if !exists {
		// Create a new index.   不存在创建一个新的
		mapping := `{
				"mappings":{
					"post":{
						"properties":{
							"location":{
								"type":"geo_point"
							}
						}
					}
				}
		}`
		_, err := client.CreateIndex(INDEX).Body(mapping).Do() //用index创建client
		if err != nil {
			// Handle error
			panic(err)
		}
	}

	fmt.Println("started-service")
	http.HandleFunc("/post", handlerPost) // 这里"/post"为endpoint  后面为执行endpoint的method 类似servlet
	http.HandleFunc("/search", handlerSearch)
	log.Fatal(http.ListenAndServe(":8080", nil)) //ListenAndServe为一个监听器  监听8080端口
}

func handlerPost(w http.ResponseWriter, r *http.Request) {
	// Parse from body of request to get a json object.
	fmt.Println("Received one post request")
	decoder := json.NewDecoder(r.Body)
	var p Post
	if err := decoder.Decode(&p); err != nil { //将从request中收到的String 转化为Post这个数据结构
		panic(err) //panic就相当于throw
	}
	fmt.Fprintf(w, "Post received: %s\n", p.Message) //Fprintf为将内容写入文件 同时有后面这个f才才能用占位%s 来操作

	id := uuid.New() //uuid保证每次都是一个unique的id
	//save to ES
	saveToES(&p, id)
}

func saveToES(p *Post, id string) {
	//create a client
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
	}

	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE).
		Id(id).
		BodyJson(p).
		Refresh(true). //有新的id时更新
		Do()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Post is saved to index: %s\n", p.Message)
}

func handlerSearch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one request for search")

	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64) //URL中get的为string 用strconv转化为float64赋值给Location
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64) //_表示不care error 不考虑返回error的情况

	ran := DISTANCE                                   //range的default为常量 DISTANCE 200KM
	if val := r.URL.Query().Get("range"); val != "" { //如果URL中提供了range 则更新为新的  否则用default 200km
		ran = val + "km"
	}

	fmt.Printf("Search received: %f %f %s\n", lat, lon, ran)

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))

	if err != nil {
		panic(err) //client相当于程序与elasticSearch连接的工具
	}

	q := elastic.NewGeoDistanceQuery("location")
	q = q.Distance(ran).Lat(lat).Lon(lon) //用获得的ran lat lon设置参数

	searchResult, err := client.Search().
		Index(INDEX).
		Query(q).     //根据之前生成的q进行搜索
		Pretty(true). //优化json格式 只是为了好看
		Do()

	if err != nil {
		panic(err)
	}

	fmt.Printf("Query took %d milliseconds\n", searchResult.TookInMillis)
	fmt.Printf("Found a total of %d posts\n", searchResult.TotalHits())

	var typ Post
	var ps []Post                                                 //将结返回成我们定义的Post类型
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) { // 前面的_为不要searchResult的index
		//go的reflect相当于java的instance of 传typ相当于把这个Post类型作为一个参数传进来 即只取类型支持Post的结果
		p := item.(Post) //相当于java中的类型转化  p = (Post)item
		fmt.Printf("Post by %s: %s at lat %v and lon %v\n", p.User, p.Message, p.Location.Lat, p.Location.Lon)

		if !containsFilteredWords(&p.Message) { //调用containsFilteredWords函数来过滤敏感词
			ps = append(ps, p)
		}

	}

	js, err := json.Marshal(ps)
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(js)
}

func containsFilteredWords(s *string) bool {
	filteredWords := []string{ //设置敏感词
		"fuck",
		"nigger",
	}
	for _, word := range filteredWords {
		if strings.Contains(*s, word) {
			return true
		}
	}
	return false
}
