from harness_fleet.slicer import slice_document


def test_slice_short_document():
    text = "Short text under 6000 chars."
    slices = slice_document(text, max_chars=100)
    assert len(slices) == 1
    assert slices[0]["slice_id"] == "full"
    assert slices[0]["partial"] is False
    assert slices[0]["text"] == text
    assert slices[0]["start"] == 0
    assert slices[0]["end"] == len(text)

def test_slice_long_document_offsets():
    text = "A" * 500 + "B" * 500 + "C" * 500
    slices = slice_document(text, max_chars=300)
    assert len(slices) > 1
    assert all(s["partial"] for s in slices)
    for s in slices:
        # Check slice bounds match actual substring
        assert text[s["start"]:s["end"]] == s["text"]
    # Lossless: every character is covered by at least one slice
    covered = bytearray(len(text))
    for s in slices:
        for i in range(s["start"], s["end"]):
            covered[i] = 1
    assert all(covered)
    assert slices[0]["start"] == 0
    assert slices[-1]["end"] == len(text)


def test_slice_long_document_overlaps_and_snaps_to_sentences():
    sentences = [f"Sentence number {i} states a verifiable fact about Kafka." for i in range(40)]
    text = " ".join(sentences)
    slices = slice_document(text, max_chars=300, overlap_chars=60)
    assert len(slices) > 1
    for prev, cur in zip(slices, slices[1:], strict=False):
        # Consecutive slices overlap (evidence spanning a cut stays whole)
        assert cur["start"] < prev["end"]
        assert text[cur["start"]:cur["end"]] == cur["text"]
    # Window ends prefer sentence boundaries over mid-sentence cuts
    for s in slices[:-1]:
        assert s["text"][-1] in ".!?\n \"')]" or s["end"] - s["start"] == 300
