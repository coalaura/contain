package main

import "unicode/utf8"

type consolePrompt struct {
	tail          [32]byte
	position      int
	lineLength    int
	unicode       [utf8.UTFMax]byte
	unicodeLength int
	escape        []byte
	intermediate  bool
}

func (prompt *consolePrompt) push(character byte) bool {
	if len(prompt.escape) != 0 {
		if len(prompt.escape) == 1 && character == '[' {
			prompt.escape = append(prompt.escape, character)

			return false
		}

		if len(prompt.escape) >= 2 {
			if character >= '@' && character <= '~' {
				prompt.escape = prompt.escape[:0]
				prompt.intermediate = false

				return false
			}

			parameter := character >= '0' && character <= '9' || character == ';' || character == '?'
			intermediate := character >= ' ' && character <= '/'

			if len(prompt.escape) < 8192 && (intermediate || parameter && !prompt.intermediate) {
				prompt.intermediate = intermediate
				prompt.escape = append(prompt.escape, character)

				return false
			}
		}

		// Invalid or oversized escape sequences remain ordinary console text.
		for _, pending := range prompt.escape {
			prompt.append(pending)
		}

		prompt.escape = prompt.escape[:0]
		prompt.intermediate = false
	}

	if character == '\x1b' {
		prompt.escape = append(prompt.escape, character)

		return false
	}

	prompt.append(character)

	return true
}

func (prompt *consolePrompt) append(character byte) {
	if character == '\r' || character == '\n' {
		prompt.lineLength = 0
		prompt.position = 0
		prompt.unicodeLength = 0

		return
	}

	prompt.tail[prompt.position] = character
	prompt.position = (prompt.position + 1) % len(prompt.tail)

	// Match the original 160-rune prefix bound without retaining the whole line.
	if character < utf8.RuneSelf && prompt.unicodeLength == 0 {
		prompt.lineLength = min(prompt.lineLength+1, 192)

		return
	}

	prompt.unicode[prompt.unicodeLength] = character
	prompt.unicodeLength++

	for prompt.unicodeLength > 0 && utf8.FullRune(prompt.unicode[:prompt.unicodeLength]) {
		_, size := utf8.DecodeRune(prompt.unicode[:prompt.unicodeLength])
		prompt.lineLength = min(prompt.lineLength+1, 192)

		copy(prompt.unicode[:], prompt.unicode[size:prompt.unicodeLength])
		prompt.unicodeLength -= size
	}
}

func (prompt *consolePrompt) suffix(text string) bool {
	if prompt.lineLength < len(text) {
		return false
	}

	for index := range len(text) {
		position := (prompt.position + len(prompt.tail) - len(text) + index) % len(prompt.tail)

		character := prompt.tail[position]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}

		if character != text[index] {
			return false
		}
	}

	return true
}

func (prompt *consolePrompt) login() bool {
	return prompt.lineLength <= 166 && prompt.suffix("login:")
}

func (prompt *consolePrompt) password() bool {
	return prompt.lineLength <= 169 && prompt.suffix("password:")
}

func (prompt *consolePrompt) shell() bool {
	return prompt.lineLength <= 161 && (prompt.suffix("#") || prompt.suffix("$"))
}

func (prompt *consolePrompt) rejected() bool {
	return prompt.suffix("login incorrect") || prompt.suffix("authentication failure") || prompt.suffix("login failed")
}
