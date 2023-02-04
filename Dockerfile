# Use an official Go image as the base image
FROM golang:latest
# Set the working directory to /app
RUN mkdir "app"
WORKDIR /app

# Copy the files to the container
COPY . /app

# Build the Go application
RUN go build -o dcbot

# Run the executable
CMD ["./dcbot"]