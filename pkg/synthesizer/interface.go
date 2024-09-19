// TODO(P1, ux): Try implementing PlayHT which seems to have superior voice: https://play.ht/" 
package synthesizer

import "github.com/petrzlen/vocode-golang/pkg/models"

/* // vocode-python
   async def create_speech(
       self,
       message: BaseMessage,
       chunk_size: int,
       bot_sentiment: Optional[BotSentiment] = None,
*/

type Synthesizer interface {
	CreateSpeech(text string, speed float64) (audioOutput models.AudioData, err error)
}
