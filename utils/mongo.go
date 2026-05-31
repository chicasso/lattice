package utils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type MClient struct {
	client *mongo.Client
	db     *mongo.Database
}

type User struct {
	ID        string    `bson:"_id" json:"_id"`
	UserName  string    `bson:"username" json:"username"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
}

func (mc *MClient) FindOne(collectionName string, query bson.D) (User, error) {
	var user User
	coll := mc.db.Collection(collectionName)

	err := coll.FindOne(context.TODO(), query).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return User{}, fmt.Errorf("no user found: %w", err)
		}
		return User{}, fmt.Errorf("failed to find user: %w", err)
	}

	return user, nil
}
