/*
	Copyright (C) 2024  Pagefault Games

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package daily

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pagefaultgames/rogueserver/db"
	"github.com/robfig/cron/v3"
)

const secondsPerDay = 60 * 60 * 24

var (
	scheduler   = cron.New(cron.WithLocation(time.UTC))
	s3scheduler = cron.New(cron.WithLocation(time.UTC))
	secret      []byte
)

func Init() error {
	var err error

	secret, err = os.ReadFile("secret.key")
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read daily seed secret: %s", err)
		}

		newSecret := make([]byte, 32)
		_, err := rand.Read(newSecret)
		if err != nil {
			return fmt.Errorf("failed to generate daily seed secret: %s", err)
		}

		err = os.WriteFile("secret.key", newSecret, 0400)
		if err != nil {
			return fmt.Errorf("failed to write daily seed secret: %s", err)
		}

		secret = newSecret
	}

	seed, err := db.TryAddDailyRun(Seed())
	if err != nil {
		log.Print(err)
	}

	log.Printf("Daily Run Seed: %s", seed)

	_, err = scheduler.AddFunc("@daily", func() {
		time.Sleep(time.Second)

		seed, err = db.TryAddDailyRun(Seed())
		if err != nil {
			log.Printf("error while recording new daily: %s", err)
		} else {
			log.Printf("Daily Run Seed: %s", seed)
		}
	})
	if err != nil {
		return err
	}

	scheduler.Start()

	if os.Getenv("AWS_ENDPOINT_URL_S3") == "" {
		log.Printf("AWS_ENDPOINT_URL_S3 not set, skipping s3 migration")
		return nil
	}

	S3SaveMigration()
	_, err = s3scheduler.AddFunc("@weekly", func() {
		time.Sleep(time.Second)
		S3SaveMigration()
	})

	if err != nil {
		return err
	}

	s3scheduler.Start()

	return nil
}

func Seed() string {
	return base64.StdEncoding.EncodeToString(deriveSeed(time.Now().UTC()))
}

func deriveSeed(seedTime time.Time) []byte {
	day := make([]byte, 8)
	binary.BigEndian.PutUint64(day, uint64(seedTime.Unix()/secondsPerDay))

	hashedSeed := md5.Sum(append(day, secret...))

	return hashedSeed[:]
}

func S3SaveMigration() {

	cfg, _ := config.LoadDefaultConfig(context.TODO())

	svc := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(os.Getenv("AWS_ENDPOINT_URL_S3"))
	})
	// retrieve accounts from db
	_, err := svc.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String("pokerogue-system"),
	})

	if err != nil {
		log.Printf("error while creating bucket: %s", err)
	}
	accounts := db.RetrieveOldAccounts()
	for _, user := range accounts {
		data, _ := db.ReadSystemSaveData(user)
		username, _ := db.FetchUsernameFromUUID(user)
		json, _ := json.Marshal(data)
		_, err := svc.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: aws.String("pokerogue-system"),
			Key:    aws.String(username),
			Body:   bytes.NewReader(json),
		})
		if err != nil {
			log.Printf("error while saving data in s3 for user %s: %s", username, err)
			continue
		}
		fmt.Printf("Saved data in s3 for user %s\n", username)
		db.UpdateLocation(user, username)
	}
}
