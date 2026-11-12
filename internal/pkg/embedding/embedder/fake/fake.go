package fake

import (
	"context"
	"fmt"
	"math"
	"math/rand"
)

// FakeEmbedder implements a simple fake embedder
type FakeEmbedder struct {
	dimension int
}

// NewFakeEmbedder creates a new fake embedder
func NewFakeEmbedder(dimension int) *FakeEmbedder {
	return &FakeEmbedder{
		dimension: dimension,
	}
}

// EmbedDocuments generates fake embedding vectors for documents
func (f *FakeEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))

	for i, text := range texts {
		// Generate deterministic fake vectors based on text length and content
		seed := int64(len(text))
		for _, char := range text {
			seed += int64(char)
		}

		rand.Seed(seed)
		embedding := make([]float32, f.dimension)

		for j := 0; j < f.dimension; j++ {
			embedding[j] = rand.Float32()*2 - 1 // Generate random numbers between -1 and 1
		}

		embeddings[i] = embedding
	}

	return embeddings, nil
}

// EmbedQuery generates fake embedding vector for a query
func (f *FakeEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := f.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return embeddings[0], nil
}

func main() {
	// Create a fake embedder with dimension 128
	embedder := NewFakeEmbedder(128)

	ctx := context.Background()

	// Sample documents
	documents := []string{
		"This is the first document",
		"This is the second document",
		"Hello world",
	}

	// Generate embeddings for documents
	docEmbeddings, err := embedder.EmbedDocuments(ctx, documents)
	if err != nil {
		fmt.Printf("Error generating document embeddings: %v\n", err)
		return
	}

	fmt.Printf("Generated %d document embeddings, each with dimension %d\n", len(docEmbeddings), len(docEmbeddings[0]))

	// Print the first 10 dimensions of the first document
	fmt.Printf("First 10 embedding values of the first document: %v\n", docEmbeddings[0][:10])

	// Generate embedding for query
	query := "Search query example"
	queryEmbedding, err := embedder.EmbedQuery(ctx, query)
	if err != nil {
		fmt.Printf("Error generating query embedding: %v\n", err)
		return
	}

	fmt.Printf("First 10 values of query embedding: %v\n", queryEmbedding[:10])

	// Simple similarity calculation example (cosine similarity)
	similarity := cosineSimilarity(queryEmbedding, docEmbeddings[0])
	fmt.Printf("Similarity between query and first document: %.4f\n", similarity)
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float32 {
	var dotProduct, normA, normB float32

	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
