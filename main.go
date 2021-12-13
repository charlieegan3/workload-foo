package main

import (
	"context"
	"embed"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"text/template"

	"github.com/cenkalti/backoff"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"gocloud.dev/blob"

	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

var awsBucket *blob.Bucket
var gcpBucket *blob.Bucket

//go:embed index.html
var indexTemplate string

//go:embed css/*
var cssContent embed.FS

func stylesHandler(w http.ResponseWriter, r *http.Request) {
	normalizeData, err := cssContent.ReadFile("css/normalize.min.css")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	tachyonsData, err := cssContent.ReadFile("css/tachyons.min.css")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	siteStyleData, err := cssContent.ReadFile("css/styles.css")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	allData := ""
	for _, b := range []*[]byte{&normalizeData, &tachyonsData, &siteStyleData} {
		allData += string(*b) + "\n"
	}

	w.Header().Set("Content-Type", "text/css")
	fmt.Fprint(w, allData)
}

func moveHandler(w http.ResponseWriter, r *http.Request) {
	var awsBucketFiles []string
	iter := awsBucket.List(nil)
	for {
		obj, err := iter.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to list items in aws bucket"))
			return
		}
		awsBucketFiles = append(awsBucketFiles, obj.Key)
	}

	var gcpBucketFiles []string
	iter = gcpBucket.List(nil)
	for {
		obj, err := iter.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to list items in gcp bucket"))
			return
		}
		gcpBucketFiles = append(gcpBucketFiles, obj.Key)
	}

	sourceBucket := awsBucket
	destinationBucket := gcpBucket
	sourceFiles := awsBucketFiles
	if len(gcpBucketFiles) > len(awsBucketFiles) {
		sourceBucket = gcpBucket
		destinationBucket = awsBucket
		sourceFiles = gcpBucketFiles
	}

	for _, file := range sourceFiles {
		sourceReader, err := sourceBucket.NewReader(context.Background(), file, nil)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to get handle for object in source bucket"))
			return
		}

		destinationWriter, err := destinationBucket.NewWriter(context.Background(), file, nil)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to get handle for object in destination bucket"))
			return
		}

		_, err = io.Copy(destinationWriter, sourceReader)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to copy file"))
			return
		}

		err = sourceReader.Close()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to close source reader"))
			return
		}
		err = destinationWriter.Close()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to close destination writer"))
			return
		}

		err = sourceBucket.Delete(context.Background(), file)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to remove the old object from source"))
			return
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	var awsBucketFiles []string
	iter := awsBucket.List(nil)
	for {
		obj, err := iter.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to list items in aws bucket"))
			return
		}
		awsBucketFiles = append(awsBucketFiles, obj.Key)
	}

	var gcpBucketFiles []string
	iter = gcpBucket.List(nil)
	for {
		obj, err := iter.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to list items in gcp bucket"))
			return
		}
		gcpBucketFiles = append(gcpBucketFiles, obj.Key)
	}

	tmplt := template.New("index")
	tmplt, err := tmplt.Parse(indexTemplate)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to parse template"))
		return
	}

	p := struct {
		AWSFiles []string
		GCPFiles []string
	}{
		AWSFiles: awsBucketFiles,
		GCPFiles: gcpBucketFiles,
	}

	tmplt.Execute(w, p)
}

func main() {
	var err error
	// load the app config
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	if err = viper.ReadInConfig(); err != nil {
		log.Fatal("config.yaml file not found")
	}

	// wait for aws creds
	operation := func() error {
		var err error
		awsBucket, err = blob.OpenBucket(context.Background(), fmt.Sprintf("s3://%s?region=%s", viper.GetString("aws.bucketName"), viper.GetString("aws.region")))
		if err != nil {
			log.Fatal(err)
		}

		accessible, err := awsBucket.IsAccessible(context.Background())
		if err != nil {
			log.Println("waiting for aws credentials")
			return err
		}

		if !accessible {
			return backoff.Permanent(fmt.Errorf("bucket was not accessible with credentials"))
		}
		return nil
	}
	err = backoff.Retry(operation, backoff.NewExponentialBackOff())
	if err != nil {
		log.Fatal(err)
		return
	}
	log.Println("aws credentials present")

	// wait for gcp creds
	operation = func() error {
		var err error

		// we check if file exists here since the blob.OpenBucket seems to
		// cache the fact that the credentials are not present, and continues
		// to fail even if present
		adcPath := "~/.config/gcloud/application_default_credentials.json"
		if s := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); s != "" {
			adcPath = s
		}
		path, err := homedir.Expand(adcPath)
		if err != nil {
			return backoff.Permanent(fmt.Errorf("failed to build path for application_default_credentials"))
		}

		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			log.Println("waiting for gcp credentials file")
			return err
		}

		gcpBucket, err = blob.OpenBucket(context.Background(), fmt.Sprintf("gs://%s", viper.GetString("gcp.bucketName")))
		if err != nil {
			log.Println(err)
			log.Println("waiting for gcp bucket handle")
			return err
		}

		accessible, err := gcpBucket.IsAccessible(context.Background())
		if err != nil || !accessible {
			return backoff.Permanent(fmt.Errorf("bucket was not accessible with credentials"))
		}
		return nil
	}
	err = backoff.Retry(operation, backoff.NewExponentialBackOff())
	if err != nil {
		log.Fatal(err)
		return
	}
	log.Println("gcp credentials present")

	// start the server
	http.HandleFunc("/styles.css", stylesHandler)
	http.HandleFunc("/move", moveHandler)
	http.HandleFunc("/", indexHandler)
	port := "3000"
	if p := viper.GetString("http.port"); p != "" {
		port = p
	}
	log.Printf("serving on port %s", port)

	err = http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
	if err != nil {
		log.Fatal(err)
	}

	err = awsBucket.Close()
	if err != nil {
		log.Fatal(err)
	}
}
