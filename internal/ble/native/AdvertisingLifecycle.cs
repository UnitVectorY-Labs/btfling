// Kept independent of WinRT so status transitions can be tested without a radio.
sealed class AdvertisingLifecycle {
    readonly object gate=new();
    readonly List<string> history=new();
    readonly TaskCompletionSource ready=new(TaskCreationOptions.RunContinuationsAsynchronously);
    readonly TaskCompletionSource<Exception> failure=new(TaskCreationOptions.RunContinuationsAsynchronously);
    bool stopping;
    public Task Ready=>ready.Task;
    public Task<Exception> Failure=>failure.Task;
    public string History { get { lock(gate){return string.Join(" -> ",history);} } }
    public void StopRequested(){lock(gate){stopping=true;}}
    public void Observe(string status,string? error) {
        lock(gate) {
            if(history.Count==8)history.RemoveAt(0);
            history.Add(status+(error==null?"":" ("+error+")"));
            if(stopping)return;
            if(status=="Started" || status=="StartedWithoutAllAdvertisementData") {
                ready.TrySetResult();return;
            }
            if(status!="Aborted" && status!="Stopped")return;
            var reason=error==null?"Windows supplied no Bluetooth error code":error;
            var exception=new IOException("BLE advertising "+status.ToLowerInvariant()+": "+reason+". Status history: "+string.Join(" -> ",history));
            failure.TrySetResult(exception);
            ready.TrySetException(exception);
        }
    }
}
