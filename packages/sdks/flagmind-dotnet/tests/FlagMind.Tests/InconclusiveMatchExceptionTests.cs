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
        Action testCode = () => throw new InconclusiveMatchException("test message");
        var thrown = Assert.Throws<InconclusiveMatchException>(testCode);
        Assert.Equal("test message", thrown.Message);
    }
}
