export type LogMessage = Record<string, unknown> & {
	_leno_id?: number;
};

export function subscribeToLiveMessages(onMessage: (message: LogMessage) => void): () => void {
	const source = new EventSource('/events');

	source.addEventListener('message', (event: MessageEvent) => {
		try {
			const message = JSON.parse(event.data as string) as LogMessage;
			onMessage(message);
		} catch (error) {
			console.error(`"${event.data}" is not a valid JSON`, error);
		}
	});

	return () => source.close();
}
