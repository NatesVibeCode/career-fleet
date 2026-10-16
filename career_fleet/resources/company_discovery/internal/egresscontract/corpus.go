package egresscontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

const CorpusPinSchema = "praxis.corpus_pin.v1"

// CorpusPin is the sealed text an infer citation may quote. Digest is the
// SHA-256 of the exact UTF-8 text. A mismatch refuses before any citation is
// trusted. Normalization and fuzzy repair are not a match.
type CorpusPin struct {
	Digest string `json:"digest"`
	Text   string `json:"text"`
}

// Citation is one exact rune span of a sealed corpus. Text must equal the
// corpus slice at those offsets. A paraphrase refuses.
type Citation struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// CorpusDigest seals text the same way harness-fleet seals a source card:
// SHA-256 of the exact bytes, lowercase hex.
func CorpusDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// SealCorpus returns the pin for text. The digest is computed, never supplied.
func SealCorpus(text string) CorpusPin {
	return CorpusPin{Digest: CorpusDigest(text), Text: text}
}

// VerifyCorpusPin refuses a digest that was not produced from the exact text.
func VerifyCorpusPin(pin CorpusPin) error {
	if len(pin.Digest) != 64 || pin.Digest != CorpusDigest(pin.Text) {
		return fmt.Errorf("corpus_pin_refused: digest does not seal the text")
	}
	return nil
}

// VerifyCitations refuses unless every citation is an exact rune span of the
// sealed text. An empty set refuses. A fuzzy or normalized near-match refuses.
func VerifyCitations(pin CorpusPin, citations []Citation) error {
	if err := VerifyCorpusPin(pin); err != nil {
		return err
	}
	if len(citations) == 0 {
		return fmt.Errorf("corpus_pin_refused: citation absent")
	}
	runes := []rune(pin.Text)
	for i, citation := range citations {
		if citation.Start < 0 || citation.End <= citation.Start || citation.End > len(runes) {
			return fmt.Errorf("corpus_pin_refused: citation %d offsets outside the sealed corpus", i)
		}
		span := string(runes[citation.Start:citation.End])
		if span != citation.Text || utf8.RuneCountInString(citation.Text) != citation.End-citation.Start {
			return fmt.Errorf("corpus_pin_refused: citation %d is not an exact span", i)
		}
	}
	return nil
}

// RequireCorpusPin is the process gate. A corpus input or a citations output
// requires the other, and every citation must be an exact span of the sealed
// text. Infer nodes that do neither are unchanged.
func RequireCorpusPin(inputs, outputs map[string]any) error {
	corpus, hasCorpus := inputs["corpus"]
	citations, hasCitations := outputs["citations"]
	if !hasCorpus && !hasCitations {
		return nil
	}
	if !hasCorpus {
		return fmt.Errorf("corpus_pin_refused: citation without a sealed corpus")
	}
	if !hasCitations {
		return fmt.Errorf("corpus_pin_refused: sealed corpus without a citation")
	}
	pin, err := decodePin(corpus)
	if err != nil {
		return err
	}
	quoted, err := decodeCitations(citations)
	if err != nil {
		return err
	}
	return VerifyCitations(pin, quoted)
}

// BindCorpusCitations applies the same gate to an inference request and its
// structured output. The corpus is local authority. It is not taken from the
// model response.
func BindCorpusCitations(req InferRequest, output any) error {
	raw, hasCitations := citationValue(output)
	if req.Corpus == nil && !hasCitations {
		return nil
	}
	if req.Corpus == nil {
		return fmt.Errorf("corpus_pin_refused: citation without a sealed corpus")
	}
	if !hasCitations {
		return fmt.Errorf("corpus_pin_refused: sealed corpus without a citation")
	}
	cited, err := decodeCitations(raw)
	if err != nil {
		return err
	}
	return VerifyCitations(*req.Corpus, cited)
}

func citationValue(output any) (any, bool) {
	object, ok := output.(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := object["citations"]
	return raw, ok
}

func decodePin(value any) (CorpusPin, error) {
	var pin CorpusPin
	if err := decodeExact(value, &pin); err != nil {
		return pin, fmt.Errorf("corpus_pin_refused: corpus")
	}
	return pin, nil
}

func decodeCitations(value any) ([]Citation, error) {
	var citations []Citation
	if err := decodeExact(value, &citations); err != nil {
		return nil, fmt.Errorf("corpus_pin_refused: citations")
	}
	return citations, nil
}

func decodeExact(value, dest any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing")
	}
	return nil
}
