package midjourney

import (
	"fmt"
	"testing"
)

func TestParseContent(t *testing.T) {
	prompt, rest, b := parseContent("**https://media.discordapp.net/attachments/981832774157762570/1094825876023152760/image.png A 200 pound kid is eating --q 2 --niji 5** - \u003c@926807951145074688\u003e (Waiting to start)")
	fmt.Println(prompt)
	fmt.Println(rest)
	fmt.Println(b)

	prompt, rest, b = parseContent("**\u003chttps://s.mj.run/LqZjmmrftcc\u003e A 200 pound kid is eating --q 2 --niji 5** - \u003c@926807951145074688\u003e (relaxed)")
	fmt.Println(prompt)
	fmt.Println(rest)
	fmt.Println(b)
}
