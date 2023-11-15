package synthesizer

/*
   async def create_speech(
       self,
       message: BaseMessage,
       chunk_size: int,
       bot_sentiment: Optional[BotSentiment] = None,
*/

type Synthesizer interface {
	CreateSpeech(text string, speed float64) (rawAudioBytes []byte, err error)
}
