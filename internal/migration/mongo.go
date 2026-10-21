package migration

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoSource struct{ database *mongo.Database }

func OpenMongo(ctx context.Context, uri, database string) (*MongoSource, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connection failed")
	}
	if err = client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo connection failed")
	}
	return &MongoSource{database: client.Database(database)}, nil
}

func (s *MongoSource) Close(ctx context.Context) error { return s.database.Client().Disconnect(ctx) }
func (s *MongoSource) Iterate(ctx context.Context, collection string, visit func(bson.M) error) error {
	cur, err := s.database.Collection(collection).Find(ctx, bson.M{})
	if err != nil {
		return fmt.Errorf("mongo read failed")
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var document bson.M
		if err = cur.Decode(&document); err != nil {
			return fmt.Errorf("mongo read failed")
		}
		if err = visit(document); err != nil {
			return err
		}
	}
	if err = cur.Err(); err != nil {
		return fmt.Errorf("mongo read failed")
	}
	return nil
}
