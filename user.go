package main

import (
	elastic "gopkg.in/olivere/elastic.v3"

	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"time"

	"github.com/dgrijalva/jwt-go"
)

const (
	TYPE_USER = "user"
)

var ( //正则表达式 规定用户名必须是小写字母或数字或_  ^代表从开始起 $代表到结束 都符合[]内的范围 +代表一个或更多
	usernamePattern = regexp.MustCompile(`^[a-z0-9_]+$`).MatchString
)

type User struct { //用户信息
	Username string `json:"username"`
	Password string `json:"password"`
	Age      int    `json:"age"`
	Gender   string `json:"gender"`
}

//check whether user is valid  从ES中根据username进行请求  得到相应的项 以User提取出来 然后对密码进行比较
func checkUser(username, password string) bool {
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false)) //user数据也存在elasticSearch
	if err != nil {
		fmt.Print("ElasticSearch is not setup %v\n", err)
		panic(err)
	}

	//search with a term query
	termQuery := elastic.NewTermQuery("username", username) //termQuery中是username
	queryResult, err := es_client.Search().
		Index(INDEX).
		Query(termQuery). //根据uesername进行query
		Pretty(true).
		Do()
	if err != nil {
		fmt.Printf("ElasticSearch query failed %v\n", err)
		panic(err)
	}

	var tyu User
	//这里用一个for loop是因为搜索之后 返回类型永远是一个slice
	for _, item := range queryResult.Each(reflect.TypeOf(tyu)) { //从ES中找到term后  从中提取出我们定义的User数据结构
		u := item.(User)
		return u.Password == password && u.Username == username //User用户名密码和传进来的都一样  return true
	}

	return false
}

// add a new user. return true if successfully
func addUser(user User) bool {
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		fmt.Printf("ElasticSearch is not setup %v\n", err)
		return false
	}

	//搜索一下ES中是否有这个用户  防止重复创建  这个过程与上面的check样
	termQuery := elastic.NewTermQuery("username", user.Username)
	queryResult, err := es_client.Search().
		Index(INDEX).
		Query(termQuery).
		Pretty(true).
		Do()
	if err != nil {
		fmt.Printf("ElasticSearch failed %v\n", err)
		return false
	}

	if queryResult.TotalHits() > 0 { //用来检测query被访问过几次  如果大于0 则ES中有这个user
		fmt.Printf("User %s already exists, cannot create a duplicate user.\n", user.Username)
		return false
	}

	//ES中无重复  则在ES中创建新的user  直接将我们定义的USER数据结作为body传入
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE_USER).
		Id(user.Username).
		BodyJson(user).
		Refresh(true).
		Do()
	if err != nil {
		fmt.Printf("ElasticSearch save user failed %v\n", err)
	}
	return true
}

// if sign up successful, a new session is created
func signupHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one signup request")

	decoder := json.NewDecoder(r.Body) //读取用户发过来的json格式的请求
	var u User
	if err := decoder.Decode(&u); err != nil { //成功decode之后放到新建的User变量中
		panic(err)
	}

	//判断用户名密码不为空且符合用户名命名规范 则add user  usernamePattern是上面定义的全局变量
	if u.Username != "" && u.Password != "" && usernamePattern(u.Username) {

		if addUser(u) { //func addUser返回值为bool
			fmt.Println("User added successfully")
			w.Write([]byte("User added successfully"))
		} else {
			fmt.Println("Failed to add a new user")
			http.Error(w, "Failed to add a new user", http.StatusInternalServerError)
		}
	} else {
		fmt.Println("Empty password or username or invalid uername")
		http.Error(w, "Empty password or username or invalid uername", http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// If login is successful, a new token is created.
func loginHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one login request")

	decoder := json.NewDecoder(r.Body)
	var u User
	if err := decoder.Decode(&u); err != nil {
		panic(err)
		return
	}

	if checkUser(u.Username, u.Password) {
		token := jwt.New(jwt.SigningMethodHS256) //生成token
		claims := token.Claims.(jwt.MapClaims)

		/* Set token claims */ // claim就是公共信息 这里用用户名和过期日期  用于用户发回时验证
		claims["username"] = u.Username
		claims["exp"] = time.Now().Add(time.Hour * 24).Unix() //Unix是UNIX时间 从1970年1月1号开始数的秒数

		/* Sign the token with our secret */
		tokenString, _ := token.SignedString(mySigningKey)

		/* Finally, write the token to the browser window */
		w.Write([]byte(tokenString))
	} else {
		fmt.Println("Invalid password or username.")
		http.Error(w, "Invalid password or username", http.StatusForbidden)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
