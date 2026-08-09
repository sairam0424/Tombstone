namespace Tombstone.Tests;
using Xunit;

public class InconclusiveMatchExceptionTests
{
    [Fact] public void IsAnException() {
        var ex = new InconclusiveMatchException("attribute missing");
        Assert.IsAssignableFrom<Exception>(ex);
        Assert.Equal("attribute missing", ex.Message);
    }

    [Fact] public void CanBeThrownAndCaught() {
        var thrown = Assert.Throws<InconclusiveMatchException>(() => {
            throw new InconclusiveMatchException("test message");
        });
        Assert.Equal("test message", thrown.Message);
    }
}
