package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/auth0/go-jwt-middleware"
	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	"github.com/pborman/uuid"
	elastic "gopkg.in/olivere/elastic.v3"
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
	Url      string   `json:"url"`
}

const ( //go中定义常量的方法  类似final
	INDEX       = "around" //因为elasticSearch可以给不同的project用 index是用来区分的
	TYPE        = "post"
	DISTANCE    = "200km"
	ES_URL      = "http://35.225.213.177:9200"
	BUCKET_NAME = "post-images-209018"
)

var mySigningKey = []byte("secret") //用token方式用户验证时server端自己定义的secret

func main() {

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false)) //与ES建立联系
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

	r := mux.NewRouter()

	var jwtMiddleware = jwtmiddleware.New(jwtmiddleware.Options{
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) { //func第一个括号是input 第二个是return
			return mySigningKey, nil
		}, //ValidationKeyGetter是获得secret 写成func可以额外再对secret做操作 这里就先简单返回secret
		SigningMethod: jwt.SigningMethodHS256, //用sha256的方式进行加密
	})

	// 这里"/post" "/search"为endpoint  后面为执行endpoint的method 类似servlet
	//收到请求后先用Router转到jwtMiddleware的Handler 来验证用户提交的signingkey是否和server的secret一样
	r.Handle("/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost))).Methods("POST")
	r.Handle("/search", jwtMiddleware.Handler(http.HandlerFunc(handlerSearch))).Methods("Get")

	//login 和 signup时需要用户名密码  还没有token 所以不用jwtMiddleware来验证
	r.Handle("/login", http.HandlerFunc(loginHandler)).Methods("POST")
	r.Handle("/signup", http.HandlerFunc(signupHandler)).Methods("POST")

	http.Handle("/", r)                          //传来的url无endpoint
	log.Fatal(http.ListenAndServe(":8080", nil)) //ListenAndServe为一个监听器  监听8080端口
}

func handlerPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	//从token的claim信息中取出用户名
	user := r.Context().Value("user")
	claims := user.(*jwt.Token).Claims
	username := claims.(jwt.MapClaims)["username"]

	// 32 << 20 is the maxMemory param for ParseMultipartForm, equals to 32MB (1MB = 1024 * 1024 bytes = 2^20 bytes)
	// After call ParseMultipartForm, the file will be saved in the server memory with maxMemory size.
	// If the file size is larger than maxMemory, the rest of the data will be saved in a system temporary file.

	r.ParseMultipartForm(32 << 20) //ParseMultipartForm为设置请求文本的最大值 <<为位操作 这代表32M 左移10位时1K 20位1M

	//Parse from data 从request中读数据

	fmt.Printf("Received one post request %s\n", r.FormValue("message"))
	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)

	p := &Post{ //将读取到的数据拼成我们自己定义的Post类型
		User:    username.(string),
		Message: r.FormValue("message"),
		Location: Location{
			Lat: lat,
			Lon: lon,
		},
	}

	//request中有两种类型  message类型和file类型   上面是读取并转换message  下面是读取file

	id := uuid.New() //uuid保证每次都是一个unique的id

	file, _, err := r.FormFile("image") //FormFile有两个返回值加一个err  File FileHeader(这个用_忽略掉)
	if err != nil {
		http.Error(w, "Image is not available", http.StatusInternalServerError) //告诉前端出错了
		fmt.Printf("Image is not available %v\n", err)                          //%v代表任何类型
		panic(err)
	}
	defer file.Close() //在整个function结束后关闭文件

	ctx := context.Background() //存取GCS时需要一个application的credenial  context是用来读credenial的

	_, attrs, err := saveToGCS(ctx, file, BUCKET_NAME, id)
	if err != nil {
		http.Error(w, "GCS is not setup", http.StatusInternalServerError)
		fmt.Printf("GCS is not setup %v\n", err)
		panic(err)
	}

	p.Url = attrs.MediaLink

	//save to ES
	saveToES(p, id)
}

func saveToGCS(ctx context.Context, r io.Reader, bucketName, name string) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	//第一个括号内为input parameter 第二个括号内为返回值类型
	//create a client
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, err //出现error  返回两个nil 一个Error
	}
	defer client.Close()

	bucket := client.Bucket(bucketName) //用buckername来创建一个bucket handle 用这个handle来访问bucket

	if _, err := bucket.Attrs(ctx); err != nil { //用传进来的credenial判断bucket是否存在  Attrs是bucket的attributions
		return nil, nil, err
	}

	//以上为连接到bucket  以下为向bucket中写入数据

	obj := bucket.Object(name) //用bucket hanle创建一个object   name为传进来的uuid
	wc := obj.NewWriter(ctx)   //用object创建一个writer  用来向bucket中写内容

	// 把要写入的文件copy到上面创建的bucket的object的writer中 则object被写成该文件
	if _, err := io.Copy(wc, r); err != nil {
		return nil, nil, err
	}
	if err = wc.Close(); err != nil {
		return nil, nil, err
	}

	//GCS文件写入之后默认是没有权限读的 所以还要修改权限 使GCS中的内容可读
	if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil { //ACL: access control list
		return nil, nil, err //AllUsers表示所有用户可以访问   RoleReader指readonly的权限
	}

	//获取所创文件的url
	attrs, err := obj.Attrs(ctx)
	fmt.Printf("Post is saved to GCS: %s\n", attrs.MediaLink)

	return obj, attrs, err
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
